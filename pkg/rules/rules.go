package rules

import (
	"context"
	"encoding/json"
	"fmt"
	kubeeyev1alpha2 "github.com/kubesphere/kubeeye/apis/kubeeye/v1alpha2"
	"github.com/kubesphere/kubeeye/pkg/constant"
	"github.com/kubesphere/kubeeye/pkg/kube"
	"github.com/kubesphere/kubeeye/pkg/template"
	"github.com/kubesphere/kubeeye/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/klog/v2"
	"os"
)

func RuleArrayDeduplication[T any](obj interface{}) []T {
	maps, err := utils.ArrayStructToArrayMap(obj)
	if err != nil {
		klog.Error(err, "Failed to convert rule to map.")
		return nil
	}
	var newMaps []map[string]interface{}
	for _, m := range maps {
		_, b, _ := utils.ArrayFinds(newMaps, func(m1 map[string]interface{}) bool {
			return m["name"] == m1["name"]
		})
		if !b {
			newMaps = append(newMaps, m)
		}
	}
	return utils.MapToStruct[T](newMaps...)

}

func Allocation(rule interface{}, taskName string, ruleType string) (*kubeeyev1alpha2.JobRule, error) {

	if rule == nil && ruleType != constant.Component {
		return nil, fmt.Errorf("failed to Allocation rule for empty")
	}

	marshalRule, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}

	return &kubeeyev1alpha2.JobRule{
		JobName:  fmt.Sprintf("%s-%s-%s", taskName, ruleType, rand.String(5)),
		RuleType: ruleType,
		RunRule:  marshalRule,
	}, nil
}

func AllocationRule(rule []interface{}, taskName string, allNode []corev1.Node, ctlOrTem string) ([]kubeeyev1alpha2.JobRule, error) {

	nodeData, filterData := utils.ArrayFilter[interface{}](rule, func(v interface{}) bool {
		return v.(map[string]interface{})["nodeName"] != nil || v.(map[string]interface{})["nodeSelector"] != nil
	})
	var jobRules []kubeeyev1alpha2.JobRule
	nodeNameMergeMap := mergeNodeRule(nodeData, allNode)
	var err error
	for _, v := range nodeNameMergeMap {
		jobRule := kubeeyev1alpha2.JobRule{
			JobName:  fmt.Sprintf("%s-%s-%s", taskName, ctlOrTem, rand.String(5)),
			RuleType: ctlOrTem,
		}
		jobRule.RunRule, err = json.Marshal(v)
		if err != nil {
			klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
			return nil, err
		}

		jobRules = append(jobRules, jobRule)
	}

	if len(filterData) > 0 {
		for _, item := range allNode {
			jobRule := kubeeyev1alpha2.JobRule{
				JobName:  fmt.Sprintf("%s-%s-%s", taskName, ctlOrTem, rand.String(5)),
				RuleType: ctlOrTem,
			}
			for i := range filterData {
				filterData[i].(map[string]interface{})["nodeName"] = &item.Name
			}
			jobRule.RunRule, err = json.Marshal(filterData)
			if err != nil {
				klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
				return nil, err
			}
			jobRules = append(jobRules, jobRule)

		}
	}

	return jobRules, nil
}

func mergeNodeRule(rule []interface{}, allNode []corev1.Node) map[string][]map[string]interface{} {
	var mergeMap = make(map[string][]map[string]interface{})
	for _, m := range rule {
		nnv, nnvExist := m.(map[string]interface{})["nodeName"]
		nsv, nsvExist := m.(map[string]interface{})["nodeSelector"]
		if nnvExist && !utils.IsEmptyValue(nnv) {
			mergeMap[nnv.(string)] = append(mergeMap[nnv.(string)], m.(map[string]interface{}))
		} else if nsvExist {
			convertMap := utils.MapValConvert[string](nsv.(map[string]interface{}))
			filterData, _ := utils.ArrayFilter(allNode, func(m corev1.Node) bool {
				isExist := false
				for k, v := range convertMap {
					isExist = m.Labels[k] == v
				}
				return isExist
			})
			for _, data := range filterData {
				copyMap := utils.DeepCopyMap(m.(map[string]interface{}))
				copyMap["nodeName"] = data.Name
				mergeMap[data.Name] = append(mergeMap[data.Name], copyMap)
			}

		}
	}
	return mergeMap
}

type ExecuteRule struct {
	KubeClient              *kube.KubernetesClient
	Task                    *kubeeyev1alpha2.InspectTask
	clusterInspectRuleMap   map[string]string
	clusterInspectRuleNames []string
	ruleTotal               map[string]int
}

func NewExecuteRuleOptions(clients *kube.KubernetesClient, Task *kubeeyev1alpha2.InspectTask) *ExecuteRule {
	clusterInspectRuleNames := []string{constant.Opa, constant.Prometheus, constant.ServiceConnect}
	clusterInspectRuleMap := map[string]string{
		"opas":           constant.Opa,
		"prometheus":     constant.Prometheus,
		"serviceConnect": constant.ServiceConnect,
		"fileChange":     constant.FileChange,
		"sysctl":         constant.Sysctl,
		"systemd":        constant.Systemd,
		"fileFilter":     constant.FileFilter,
		"customCommand":  constant.CustomCommand,
		"nodeInfo":       constant.NodeInfo,
	}
	return &ExecuteRule{
		KubeClient:              clients,
		Task:                    Task,
		clusterInspectRuleNames: clusterInspectRuleNames,
		clusterInspectRuleMap:   clusterInspectRuleMap,
	}
}

func (e *ExecuteRule) SetRuleSchedule(rules []kubeeyev1alpha2.InspectRule) (newRules []kubeeyev1alpha2.InspectRule) {
	for _, r := range e.Task.Spec.RuleNames {
		_, isExist, rule := utils.ArrayFinds(rules, func(m kubeeyev1alpha2.InspectRule) bool {
			return r.Name == m.Name
		})
		if isExist {
			if !utils.IsEmptyValue(r.NodeName) || r.NodeSelector != nil {
				toMap := utils.StructToMap(rule.Spec)
				if toMap != nil {
					for _, v := range toMap {
						switch val := v.(type) {
						case []interface{}:
							for i := range val {
								m := val[i].(map[string]interface{})
								_, nnExist := m["nodeName"]
								_, nsExist := m["nodeSelector"]
								if !nnExist && !nsExist {
									m["nodeName"] = r.NodeName
									m["nodeSelector"] = r.NodeSelector
								}
							}
						}
					}
					rule.Spec = utils.MapToStruct[kubeeyev1alpha2.InspectRuleSpec](toMap)[0]
				}

			}
			newRules = append(newRules, rule)
		}
	}
	return newRules
}

func (e *ExecuteRule) SetPrometheusEndpoint(allRule []kubeeyev1alpha2.InspectRule) []kubeeyev1alpha2.InspectRule {
	for i := range allRule {
		if !utils.IsEmptyValue(allRule[i].Spec.PrometheusEndpoint) && allRule[i].Spec.Prometheus != nil {
			for p := range allRule[i].Spec.Prometheus {
				if utils.IsEmptyValue(allRule[i].Spec.Prometheus[p].Endpoint) {
					allRule[i].Spec.Prometheus[p].Endpoint = allRule[i].Spec.PrometheusEndpoint
				}
			}
		}
	}
	return allRule
}

func (e *ExecuteRule) MergeRule(allRule []kubeeyev1alpha2.InspectRule) (map[string][]interface{}, error) {
	//var newRuleSpec kubeeyev1alpha2.InspectRuleSpec
	var newSpecMap = make(map[string][]interface{})
	ruleTotal := map[string]int{constant.Component: 1}
	for _, rule := range e.SetPrometheusEndpoint(e.SetRuleSchedule(allRule)) {
		toMap := utils.StructToMap(rule.Spec)
		for k, v := range toMap {
			switch val := v.(type) {
			case []interface{}:
				dedupMap := RuleArrayDeduplication[interface{}](append(newSpecMap[k], val...))
				if dedupMap != nil {
					newSpecMap[k] = dedupMap
					ruleTotal[e.clusterInspectRuleMap[k]] = len(newSpecMap[k])
				} else {
					newSpecMap[k] = append(newSpecMap[k], val...)
				}
			}
		}
	}

	//if rule.Spec.ServiceConnect != nil && newRuleSpec.ServiceConnect == nil {
	//	newRuleSpec.ServiceConnect = rule.Spec.ServiceConnect
	//	component, err := inspect.GetInspectComponent(context.TODO(), e.KubeClient, newRuleSpec.ServiceConnect)
	//	if err != nil {
	//		ruleTotal[constant.ServiceConnect] = 0
	//	} else {
	//		ruleTotal[constant.ServiceConnect] = len(component)
	//	}
	//
	//}
	//convert := utils.ArrayValConvert[string](newSpecMap["componentExclude"])
	//for _, namespace := range constant.SystemNamespaces {
	//	list, err := e.KubeClient.ClientSet.CoreV1().Services(namespace).List(context.TODO(), metav1.ListOptions{})
	//	if err == nil {
	//		for _, item := range list.Items {
	//			if len(item.Spec.Selector) > 0 && !slices.Contains(convert, item.Name) {
	//				ruleTotal[constant.Component] += 1
	//			}
	//		}
	//	}
	//}

	//marshal, err := json.Marshal(newSpecMap)
	//if err != nil {
	//	return newRuleSpec, err
	//}
	//err = json.NewDecoder(bytes.NewReader(marshal)).Decode(&newRuleSpec)
	//if err != nil {
	//	return newRuleSpec, err
	//}
	e.ruleTotal = ruleTotal
	return newSpecMap, nil
}

func (e *ExecuteRule) GenerateJob(ctx context.Context, rulesSpec map[string][]interface{}) (jobs []kubeeyev1alpha2.JobRule) {

	//toMap := utils.StructToMap(rulesSpec)
	nodes := kube.GetNodes(ctx, e.KubeClient.ClientSet)
	for key, v := range rulesSpec {
		mapV, mapExist := e.clusterInspectRuleMap[key]
		if mapExist {
			_, exist := utils.ArrayFind(mapV, e.clusterInspectRuleNames)
			if exist {
				allocation, err := Allocation(v, e.Task.Name, mapV)
				if err == nil {
					jobs = append(jobs, *allocation)
				}
			} else {
				allocationRule, err := AllocationRule(v, e.Task.Name, nodes, mapV)
				if err == nil {
					jobs = append(jobs, allocationRule...)
				}
			}
		}
	}

	component, err := Allocation(rulesSpec["componentExclude"], e.Task.Name, constant.Component)
	if err == nil {
		jobs = append(jobs, *component)
	}

	return jobs
}

func (e *ExecuteRule) CreateInspectRule(ctx context.Context, ruleGroup []kubeeyev1alpha2.JobRule) ([]kubeeyev1alpha2.JobRule, error) {
	r := sortRuleOpaToAtLast(ruleGroup)
	ruleData, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	_, err = e.KubeClient.ClientSet.CoreV1().ConfigMaps(os.Getenv("KUBERNETES_POD_NAMESPACE")).Get(ctx, e.Task.Name, metav1.GetOptions{})
	if err == nil {
		_ = e.KubeClient.ClientSet.CoreV1().ConfigMaps(os.Getenv("KUBERNETES_POD_NAMESPACE")).Delete(ctx, e.Task.Name, metav1.DeleteOptions{})
	}
	// create temp inspect rule
	configMapTemplate := template.BinaryConfigMapTemplate(e.Task.Name, os.Getenv("KUBERNETES_POD_NAMESPACE"), ruleData, true, map[string]string{constant.LabelInspectRuleGroup: "inspect-rule-temp"})
	_, err = e.KubeClient.ClientSet.CoreV1().ConfigMaps(os.Getenv("KUBERNETES_POD_NAMESPACE")).Create(ctx, configMapTemplate, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func sortRuleOpaToAtLast(rule []kubeeyev1alpha2.JobRule) []kubeeyev1alpha2.JobRule {

	finds, b, OpaRule := utils.ArrayFinds(rule, func(i kubeeyev1alpha2.JobRule) bool {
		return i.RuleType == constant.Opa
	})
	if b {
		rule = append(rule[:finds], rule[finds+1:]...)
		rule = append(rule, OpaRule)
	}

	return rule
}

func (e *ExecuteRule) GetRuleTotal() map[string]int {
	return e.ruleTotal
}
