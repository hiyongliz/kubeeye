/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kubesphere/kubeeye/pkg/constant"
	"github.com/kubesphere/kubeeye/pkg/rules"
	"github.com/kubesphere/kubeeye/pkg/template"
	"github.com/kubesphere/kubeeye/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"math"
	"os"
	"path"
	"sync"
	"time"

	kubeeyev1alpha2 "github.com/kubesphere/kubeeye/apis/kubeeye/v1alpha2"
	"github.com/kubesphere/kubeeye/pkg/conf"
	"github.com/kubesphere/kubeeye/pkg/inspect"
	"github.com/kubesphere/kubeeye/pkg/kube"
	kubeErr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InspectTaskReconciler reconciles a InspectTask object
type InspectTaskReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	K8sClients *kube.KubernetesClient
}

//+kubebuilder:rbac:groups=kubeeye.kubesphere.io,resources=inspecttasks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cluster.kubesphere.io,resources=clusters,verbs=get
//+kubebuilder:rbac:groups=kubeeye.kubesphere.io,resources=inspecttasks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kubeeye.kubesphere.io,resources=inspecttasks/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;deletecollection;list;get
//+kubebuilder:rbac:groups="",resources=nodes;services;pods;events,verbs=list;get
//+kubebuilder:rbac:groups="",resources=namespaces;serviceaccounts,verbs=list;get;create
//+kubebuilder:rbac:groups="apps",resources="*",verbs=get;list
//+kubebuilder:rbac:groups="batch",resources="*",verbs=get;list;create;delete
//+kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources="*",verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the InspectTask object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *InspectTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//logger := log.FromContext(ctx).WithName(req.NamespacedName.String())

	inspectTask := &kubeeyev1alpha2.InspectTask{}
	err := r.Get(ctx, req.NamespacedName, inspectTask)
	if err != nil {
		if kubeErr.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		klog.Error("failed to get inspect task. ", err)
		return ctrl.Result{}, err
	}

	if inspectTask.DeletionTimestamp.IsZero() {
		if _, b := utils.ArrayFind(Finalizers, inspectTask.Finalizers); !b {
			inspectTask.Finalizers = append(inspectTask.Finalizers, Finalizers)
			err = r.Client.Update(ctx, inspectTask)
			if err != nil {
				klog.Error("Failed to inspect plan add finalizers", err)
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

	} else {
		newFinalizers := utils.SliceRemove(Finalizers, inspectTask.Finalizers)
		inspectTask.Finalizers = newFinalizers.([]string)
		klog.Infof("inspect task is being deleted")
		err = r.Client.Update(ctx, inspectTask)
		if err != nil {
			klog.Error("Failed to inspect plan add finalizers. ", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if inspectTask.Status.StartTimestamp.IsZero() {
		inspectTask.Status.StartTimestamp = metav1.Time{Time: time.Now()}
		inspectTask.Status.ClusterInfo, err = r.getClusterInfo(ctx)
		if err != nil {
			klog.Error("failed to get cluster info. ", err)
			return ctrl.Result{}, err
		}
		kubeEyeConfig, err := kube.GetKubeEyeConfig(ctx, r.K8sClients)
		if err != nil {
			klog.Error("Unable to get jobConfig")
			return ctrl.Result{}, err
		}
		err = r.updatePlanStatus(ctx, kubeeyev1alpha2.PhaseRunning, inspectTask.GetLabels()[constant.LabelName], inspectTask.Name)
		if err != nil {
			klog.Error("Failed to update inspect plan status. ", err)
			return ctrl.Result{}, err
		}

		getRules, err := r.getRules(ctx, inspectTask)

		if err != nil {
			klog.Error("failed get to inspectrules.", err)
			return ctrl.Result{}, err
		}

		if inspectTask.Spec.ClusterName != nil {
			var wait sync.WaitGroup
			wait.Add(len(inspectTask.Spec.ClusterName))
			for _, name := range inspectTask.Spec.ClusterName {
				go func(v string) {
					defer wait.Done()
					clusterClient, err := kube.GetMultiClusterClient(ctx, r.K8sClients, v)
					if err != nil {
						klog.Error(err, "Failed to get multi-cluster client.")
						return
					}
					err = r.initClusterInspect(ctx, clusterClient)
					if err != nil {
						klog.Errorf("failed To Initialize Cluster Configuration for Cluster Name:%s,err:%s", v, err)
						return
					}
					err = r.CreateInspect(ctx, v, inspectTask, *getRules, clusterClient, kubeEyeConfig)
					if err != nil {
						klog.Error("failed to create inspect. ", err)
					}
				}(name)
			}
			wait.Wait()
		} else {
			err := r.CreateInspect(ctx, "default", inspectTask, *getRules, r.K8sClients, kubeEyeConfig)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		klog.Infof("all job finished for taskName:%s", inspectTask.Name)

		err = r.Status().Update(ctx, inspectTask)
		if err != nil {
			klog.Error("failed to update inspect task. ", err)
			return ctrl.Result{}, err
		}
		getStatus := GetStatus(inspectTask)
		err = r.updatePlanStatus(ctx, getStatus, inspectTask.GetLabels()[constant.LabelName], inspectTask.Name)
		if err != nil {
			klog.Error("failed to update inspect plan comeToAnEnd status. ", err)
		}

		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

func createInspectRule(ctx context.Context, clients *kube.KubernetesClient, ruleGroup []kubeeyev1alpha2.JobRule, task *kubeeyev1alpha2.InspectTask) ([]kubeeyev1alpha2.JobRule, error) {
	r := sortRuleOpaToAlter(ruleGroup)
	marshal, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	_, err = clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).Get(ctx, task.Name, metav1.GetOptions{})
	if err == nil {
		_ = clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).Delete(ctx, task.Name, metav1.DeleteOptions{})
	}

	configMapTemplate := template.BinaryConfigMapTemplate(task.Name, constant.DefaultNamespace, marshal, true, map[string]string{constant.LabelInspectRuleGroup: "inspect-rule-temp"})
	_, err = clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).Create(ctx, configMapTemplate, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *InspectTaskReconciler) CreateInspect(ctx context.Context, name string, task *kubeeyev1alpha2.InspectTask, ruleLists kubeeyev1alpha2.InspectRuleList, clients *kube.KubernetesClient, kubeEyeConfig conf.KubeEyeConfig) error {

	inspectRule, inspectRuleNum, err := rules.ParseRules(ctx, clients, task.Name, ruleLists.Items)
	if err != nil {
		return err
	}
	rule, err := createInspectRule(ctx, clients, inspectRule, task)
	if err != nil {
		return err
	}
	JobPhase, err := r.createJobsInspect(ctx, task, clients, kubeEyeConfig.Job, rule)
	if err != nil {
		return err
	}
	task.Status.JobPhase = append(task.Status.JobPhase, JobPhase...)
	task.Status.EndTimestamp = metav1.Time{Time: time.Now()}
	err = r.GetInspectResultData(ctx, clients, task, name, inspectRuleNum)
	if err != nil {
		return err
	}
	return nil
}

func sortRuleOpaToAlter(rule []kubeeyev1alpha2.JobRule) []kubeeyev1alpha2.JobRule {

	finds, b, OpaRule := utils.ArrayFinds(rule, func(i kubeeyev1alpha2.JobRule) bool {
		return i.RuleType == constant.Opa
	})
	if b {
		rule = append(rule[:finds], rule[finds+1:]...)
		rule = append(rule, OpaRule)
	}

	return rule
}
func GetStatus(task *kubeeyev1alpha2.InspectTask) kubeeyev1alpha2.Phase {
	if task.Status.JobPhase == nil {
		return kubeeyev1alpha2.PhaseFailed
	}
	_, ok, _ := utils.ArrayFinds(task.Status.JobPhase, func(m kubeeyev1alpha2.JobPhase) bool {
		return m.Phase == kubeeyev1alpha2.PhaseFailed
	})
	if ok {
		return kubeeyev1alpha2.PhaseFailed
	}
	return kubeeyev1alpha2.PhaseSucceeded
}

func (r *InspectTaskReconciler) getClusterInfo(ctx context.Context) (kubeeyev1alpha2.ClusterInfo, error) {
	var clusterInfo kubeeyev1alpha2.ClusterInfo
	versionInfo, err := r.K8sClients.ClientSet.Discovery().ServerVersion()
	if err != nil {
		klog.Error(err, "Failed to get Kubernetes serverVersion.\n")
	}
	var serverVersion string
	if versionInfo != nil {
		serverVersion = versionInfo.Major + "." + versionInfo.Minor
	}
	_, nodesCount, err := kube.GetObjectCounts(ctx, r.K8sClients, conf.Nodes, conf.NoGroup)
	if err != nil {
		klog.Error(err, "Failed to get node number.")
	}
	_, namespacesCount, err := kube.GetObjectCounts(ctx, r.K8sClients, conf.Namespaces, conf.NoGroup)
	if err != nil {
		klog.Error(err, "Failed to get ns number.")
	}
	clusterInfo = kubeeyev1alpha2.ClusterInfo{ClusterVersion: serverVersion, NodesCount: nodesCount, NamespacesCount: namespacesCount}
	return clusterInfo, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *InspectTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeeyev1alpha2.InspectTask{}).
		Complete(r)
}

func (r *InspectTaskReconciler) createJobsInspect(ctx context.Context, inspectTask *kubeeyev1alpha2.InspectTask, clusterClient *kube.KubernetesClient, config *conf.JobConfig, inspectRule []kubeeyev1alpha2.JobRule) ([]kubeeyev1alpha2.JobPhase, error) {
	var jobNames []kubeeyev1alpha2.JobPhase
	nodes := kube.GetNodes(ctx, clusterClient.ClientSet)
	concurrency := 5
	runNumber := math.Round(float64(len(nodes)) + float64(len(inspectRule))*0.1)
	if runNumber > 5 {
		concurrency = int(runNumber)
	}
	var wg sync.WaitGroup
	var mutex sync.Mutex
	semaphore := make(chan struct{}, concurrency)
	for _, rule := range inspectRule {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(v kubeeyev1alpha2.JobRule) {
			defer func() {
				wg.Done()
				<-semaphore
			}()
			if isTimeout(inspectTask.CreationTimestamp, inspectTask.Spec.Timeout) {
				jobNames = append(jobNames, kubeeyev1alpha2.JobPhase{JobName: v.JobName, Phase: kubeeyev1alpha2.PhaseFailed})
				return
			}
			if err := isExistsJob(ctx, clusterClient, v.JobName); err != nil {
				mutex.Lock()
				jobNames = append(jobNames, kubeeyev1alpha2.JobPhase{JobName: v.JobName, Phase: kubeeyev1alpha2.PhaseSucceeded})
				mutex.Unlock()
				return
			}

			inspectInterface, status := inspect.RuleOperatorMap[v.RuleType]
			if status {
				klog.Infof("Job %s created", v.JobName)
				jobTask, err := inspectInterface.CreateJobTask(ctx, clusterClient, &v, inspectTask, config)
				if err != nil {
					klog.Errorf("create job error. error:%s", err)
					jobNames = append(jobNames, kubeeyev1alpha2.JobPhase{JobName: v.JobName, Phase: kubeeyev1alpha2.PhaseFailed})
					return
				}
				resultJob := r.waitForJobCompletionGetResult(ctx, clusterClient, v.JobName, jobTask, inspectTask.Spec.Timeout)
				mutex.Lock()
				jobNames = append(jobNames, *resultJob)
				mutex.Unlock()
				klog.Infof("Job %s completed", v.JobName)
			} else {
				klog.Errorf("%s not found", v.RuleType)
			}

		}(rule)
	}
	wg.Wait()

	err := r.clearRule(ctx, clusterClient, inspectTask.Spec.ClusterName)
	if err != nil {
		return nil, err
	}
	return jobNames, nil
}

func isExistsJob(ctx context.Context, clients *kube.KubernetesClient, jobName string) error {
	_, err := clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil && kubeErr.IsNotFound(err) {
		return nil
	}
	klog.Errorf("job already exists for name:%s", jobName)
	return fmt.Errorf("job already exists for name:%s", jobName)
}

func (r *InspectTaskReconciler) waitForJobCompletionGetResult(ctx context.Context, clients *kube.KubernetesClient, jobName string, jobPhase *kubeeyev1alpha2.JobPhase, timeout string) *kubeeyev1alpha2.JobPhase {

	for {
		klog.Infof("wait job run complete for name:%s", jobName)
		jobInfo, err := clients.ClientSet.BatchV1().Jobs(constant.DefaultNamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			jobPhase.Phase = kubeeyev1alpha2.PhaseFailed
			return jobPhase
		}
		if isTimeout(jobInfo.CreationTimestamp, timeout) {
			klog.Infof("timeout for name:%s", jobName)
			jobPhase.Phase = kubeeyev1alpha2.PhaseFailed
			background := metav1.DeletePropagationBackground
			_ = clients.ClientSet.BatchV1().Jobs(constant.DefaultNamespace).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &background})
			return jobPhase
		}
		if jobInfo.Status.CompletionTime != nil && !jobInfo.Status.CompletionTime.IsZero() && jobInfo.Status.Active == 0 {
			jobPhase.Phase = kubeeyev1alpha2.PhaseSucceeded
			return jobPhase
		}
		time.Sleep(10 * time.Second)
	}

}

func (r *InspectTaskReconciler) GetInspectResultData(ctx context.Context, clients *kube.KubernetesClient, task *kubeeyev1alpha2.InspectTask, clusterName string, ruleNum map[string]int) error {
	configs, err := clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.FormatLabels(map[string]string{constant.LabelTaskName: task.Name}),
	})

	if err != nil {
		return err
	}
	var ownerRefBol = true
	inspectResult := kubeeyev1alpha2.InspectResult{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-result", clusterName, task.Name),
			Labels: map[string]string{constant.LabelTaskName: task.Name},
			Annotations: map[string]string{
				constant.AnnotationStartTime:      task.Status.StartTimestamp.Format("2006-01-02 15:04:05"),
				constant.AnnotationEndTime:        task.Status.EndTimestamp.Format("2006-01-02 15:04:05"),
				constant.AnnotationInspectPolicy:  string(task.Spec.InspectPolicy),
				constant.AnnotationInspectCluster: clusterName,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         task.APIVersion,
				Kind:               task.Kind,
				Name:               task.Name,
				UID:                task.UID,
				Controller:         &ownerRefBol,
				BlockOwnerDeletion: &ownerRefBol,
			}},
		},
		Spec: kubeeyev1alpha2.InspectResultSpec{
			InspectRuleTotal: ruleNum,
		},
	}

	resultData := inspectResult.DeepCopy()
	for _, phase := range task.Status.JobPhase {
		if phase.Phase == kubeeyev1alpha2.PhaseSucceeded {
			_, exists, configMap := utils.ArrayFinds(configs.Items, func(m corev1.ConfigMap) bool {
				return m.Name == phase.JobName
			})
			if exists {
				ruleType := configMap.GetLabels()[constant.LabelRuleType]
				nodeName := configMap.GetLabels()[constant.LabelNodeName]
				inspectInterface, status := inspect.RuleOperatorMap[ruleType]
				if status {
					klog.Infof("starting get %s result data", phase.JobName)
					_, err = inspectInterface.GetResult(nodeName, &configMap, resultData)
					if err != nil {
						klog.Error(err)
					}
				}
			}
		}
	}
	err = saveResultFile(resultData)
	if err != nil {
		return err
	}

	err = r.Create(ctx, &inspectResult)
	if err != nil {
		return err
	}

	err = clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labels.FormatLabels(map[string]string{constant.LabelTaskName: task.Name})})
	if err != nil {
		return err
	}

	return nil
}

func saveResultFile(resultData *kubeeyev1alpha2.InspectResult) error {
	file, err := os.OpenFile(path.Join(constant.ResultPath, resultData.Name), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		klog.Error(err, "open file error")
		return err
	}
	defer file.Close()
	marshal, err := json.Marshal(resultData)
	if err != nil {
		klog.Error(err, "marshal error")
	}
	_, err = file.Write(marshal)
	if err != nil {
		klog.Error(err, "write file error")
		return err
	}
	return nil
}

func isTimeout(startTime metav1.Time, t string) bool {
	duration, err := time.ParseDuration(t)
	if err != nil {
		duration = constant.DefaultTimeout
	}
	return startTime.Add(duration).Before(time.Now())
}

// InitClusterInspect Initialize the relevant configuration items required for multi-cluster inspection
func (r *InspectTaskReconciler) initClusterInspect(ctx context.Context, clients *kube.KubernetesClient) error {
	_, err := clients.ClientSet.CoreV1().Namespaces().Get(ctx, constant.DefaultNamespace, metav1.GetOptions{})
	if err != nil {
		if kubeErr.IsNotFound(err) {
			_, err = clients.ClientSet.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: constant.DefaultNamespace}}, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	_, err = clients.ClientSet.RbacV1().ClusterRoles().Get(ctx, "kubeeye-manager-role", metav1.GetOptions{})
	if err != nil {
		if kubeErr.IsNotFound(err) {
			clusterRole, err := r.K8sClients.ClientSet.RbacV1().ClusterRoles().Get(ctx, "kubeeye-manager-role", metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = clients.ClientSet.RbacV1().ClusterRoles().Create(ctx, &v1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: clusterRole.Name},
				Rules:      clusterRole.Rules,
			}, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	_, err = clients.ClientSet.RbacV1().ClusterRoleBindings().Get(ctx, "kubeeye-manager-rolebinding", metav1.GetOptions{})
	if err != nil {
		if kubeErr.IsNotFound(err) {
			clusterRoleBinding, err := r.K8sClients.ClientSet.RbacV1().ClusterRoleBindings().Get(ctx, "kubeeye-manager-rolebinding", metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = clients.ClientSet.RbacV1().ClusterRoleBindings().Create(ctx, &v1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterRoleBinding.Name},
				Subjects:   clusterRoleBinding.Subjects,
				RoleRef:    clusterRoleBinding.RoleRef,
			}, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	_, err = clients.ClientSet.CoreV1().ServiceAccounts(constant.DefaultNamespace).Get(ctx, "kubeeye-controller-manager", metav1.GetOptions{})

	if err != nil {
		if kubeErr.IsNotFound(err) {
			serviceAccount, err := r.K8sClients.ClientSet.CoreV1().ServiceAccounts(constant.DefaultNamespace).Get(ctx, "kubeeye-controller-manager", metav1.GetOptions{})
			if err != nil {
				return err
			}
			serviceAccountNew := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceAccount.Name,
					Namespace: serviceAccount.Namespace,
				},
			}
			_, err = clients.ClientSet.CoreV1().ServiceAccounts(constant.DefaultNamespace).Create(ctx, serviceAccountNew, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		} else {
			return err
		}

	}

	return nil
}

func (r *InspectTaskReconciler) clearRule(ctx context.Context, clients *kube.KubernetesClient, clusterName []string) error {
	return clients.ClientSet.CoreV1().ConfigMaps(constant.DefaultNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: labels.FormatLabels(map[string]string{constant.LabelInspectRuleGroup: "inspect-rule-temp"}),
	})
}

func (r *InspectTaskReconciler) updatePlanStatus(ctx context.Context, phase kubeeyev1alpha2.Phase, planName string, taskName string) error {
	plan := &kubeeyev1alpha2.InspectPlan{}
	err := r.Get(ctx, types.NamespacedName{Name: planName}, plan)
	if err != nil {
		klog.Error(err, "get plan error")
		return err
	}
	for i, name := range plan.Status.TaskNames {
		if name.Name == taskName {
			plan.Status.TaskNames[i].TaskStatus = phase
			break
		}
	}
	plan.Status.LastTaskStatus = phase
	err = r.Status().Update(ctx, plan)
	if err != nil {
		klog.Error(err, "update plan status error")
		return err
	}
	return nil
}

func (r *InspectTaskReconciler) getRules(ctx context.Context, task *kubeeyev1alpha2.InspectTask) (*kubeeyev1alpha2.InspectRuleList, error) {
	ruleList, err := r.K8sClients.VersionClientSet.KubeeyeV1alpha2().InspectRules().List(ctx, metav1.ListOptions{
		LabelSelector: labels.FormatLabels(map[string]string{constant.LabelRuleGroup: task.Labels[constant.LabelRuleGroup]}),
	})
	if err != nil {
		return nil, err
	}

	return ruleList, nil
}