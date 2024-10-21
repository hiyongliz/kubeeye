package output

import (
	"context"
	kubeeyev1alpha2 "github.com/kubesphere/kubeeye/apis/kubeeye/v1alpha2"
	"github.com/kubesphere/kubeeye/pkg/kube"
	v1 "k8s.io/api/core/v1"
	"k8s.io/utils/strings/slices"
	"strconv"
	"strings"
)

func ParseCustomizedStruct(ctx context.Context, client *kube.KubernetesClient, data *kubeeyev1alpha2.InspectResult) map[string]interface{} {

	metric := ParseOtherMetric(data)

	var nodes []v1.Node
	if data.Spec.InspectCluster.Name == "default" {
		nodes = kube.GetNodes(ctx, client.ClientSet)
	} else {
		clusterClient, err := kube.GetMultiClusterClient(ctx, client, data.Spec.InspectCluster.Name)
		if err == nil {
			nodes = kube.GetNodes(ctx, clusterClient.ClientSet)
		}
	}

	return map[string]interface{}{
		"name":                data.Name,
		"cluster":             data.Spec.InspectCluster.Name,
		"overview_count":      OverviewCount(metric, data.Spec.ComponentResult),
		"component_status":    data.Spec.ComponentResult,
		"api_status":          ParseApiStatus(data),
		"resources_usage":     ParseResources(data),
		"resources_usage_top": ParseResourcesTop(data),
		"metric":              metric,
		"nodes_status":        ParseNodeStatus(nodes)}
}

func ParseNodeStatus(nodes []v1.Node) map[string]int {
	nodeStatus := map[string]int{"total": len(nodes), "ready": 0, "not_ready": 0, "no_schedule": 0}

	IsNodesSchedule := func(node v1.Node) bool {
		for _, taint := range node.Spec.Taints {
			if taint.Key == "node.kubernetes.io/unschedulable" && taint.Effect == v1.TaintEffectNoSchedule {
				return true
			}
		}
		return false
	}

	for _, node := range nodes {
		if !kube.IsNodesReady(node) {
			nodeStatus["not_ready"]++
		} else if IsNodesSchedule(node) {
			nodeStatus["no_schedule"]++
		} else {
			nodeStatus["ready"]++
		}
	}

	return nodeStatus
}
func ParseApiStatus(result *kubeeyev1alpha2.InspectResult) map[string]string {
	apiStatus := make(map[string]string, 2)
	for _, pro := range result.Spec.PrometheusResult {
		if strings.ToUpper(pro.Name) == strings.ToUpper("apiserver_request_latencies") {
			apiStatus["apiserver_request_latencies"] = pro.ParseString()["value"]
		}
		if strings.ToUpper(pro.Name) == strings.ToUpper("apiserver_request_rate") {
			apiStatus["apiserver_request_rate"] = pro.ParseString()["value"]
		}

	}
	return apiStatus
}
func ParseResources(result *kubeeyev1alpha2.InspectResult) map[string]map[string]float64 {
	metricData := make(map[string]float64)
	metrics := []string{
		"cluster_cpu_usage",
		"cluster_cpu_total",
		"cluster_memory_usage_wo_cache",
		"cluster_memory_total",
		"cluster_disk_size_usage",
		"cluster_disk_size_capacity",
		"cluster_pod_running_count",
		"cluster_pod_quota",
		"cluster_pod_memory_requests_total",
		"cluster_pod_memory_limits_total",
		"cluster_pod_cpu_requests_total",
		"cluster_pod_cpu_limits_total"}
	for _, pro := range result.Spec.PrometheusResult {
		if slices.Contains(metrics, strings.ToLower(pro.Name)) {
			float, err := strconv.ParseFloat(pro.ParseString()["value"], 64)
			if err != nil {
				float = 0
			}
			metricData[pro.Name] = float
		}
	}

	resourcesData := make(map[string]map[string]float64)
	if len(metricData) > 0 {
		resourcesData["cpu"] = map[string]float64{"total": metricData["cluster_cpu_total"], "usage": metricData["cluster_cpu_usage"], "cluster_pod_cpu_limits_total": metricData["cluster_pod_cpu_limits_total"], "cluster_pod_cpu_requests_total": metricData["cluster_pod_cpu_requests_total"], "percent": metricComputed(metricData["cluster_cpu_usage"], metricData["cluster_cpu_total"])}
		resourcesData["memory"] = map[string]float64{"total": metricData["cluster_memory_total"], "usage": metricData["cluster_memory_usage_wo_cache"], "cluster_pod_memory_requests_total": metricData["cluster_pod_memory_requests_total"], "cluster_pod_memory_limits_total": metricData["cluster_pod_memory_limits_total"], "percent": metricComputed(metricData["cluster_memory_usage_wo_cache"], metricData["cluster_memory_total"])}
		resourcesData["disk"] = map[string]float64{"total": metricData["cluster_disk_size_capacity"], "usage": metricData["cluster_disk_size_usage"], "percent": metricComputed(metricData["cluster_disk_size_usage"], metricData["cluster_disk_size_capacity"])}
		resourcesData["pod"] = map[string]float64{"total": metricData["cluster_pod_quota"], "usage": metricData["cluster_pod_running_count"], "percent": metricComputed(metricData["cluster_pod_running_count"], metricData["cluster_pod_quota"])}
	}
	return resourcesData
}

func ParseResourcesTop(result *kubeeyev1alpha2.InspectResult) map[string]map[string]string {
	resourcesTopMetrics := []string{"node_cpu_utilisation", "node_memory_utilisation", "node_disk_size_utilisation"}
	metricsTop := make(map[string]map[string]string)
	for _, r := range result.Spec.PrometheusResult {
		if slices.Contains(resourcesTopMetrics, strings.ToLower(r.Name)) {
			p := r.ParseString()
			m := metricsTop[r.Name]
			if m == nil {
				metricsTop[r.Name] = map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]}
			} else {
				mF, err := strconv.ParseFloat(m["value"], 64)
				if err != nil {
					mF = 0
				}
				pF, err := strconv.ParseFloat(p["value"], 64)
				if err != nil {
					pF = 0
				}
				if mF < pF {
					metricsTop[r.Name] = map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]}
				}
			}
		}
	}
	return metricsTop
}
func ParseOtherMetric(result *kubeeyev1alpha2.InspectResult) map[string][]map[string]string {
	otherMetricData := make(map[string][]map[string]string)
	for _, r := range result.Spec.PrometheusResult {
		if strings.ToLower(r.Name) == strings.ToLower("node_load_15") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("filesystem_readonly") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["instance"], "mountpoint": p["mountpoint"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("filesystem_avail") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["instance"], "mountpoint": p["mountpoint"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("harbor_health") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "name": p["name"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("harbor_ref_work_replication") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("node_loadapp_etcd_backup_status") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["instance"], "value": p["value"]})
		}

		// add new metric
		// "node_pod_usage_ratio", "node_memory_usage_ratio", "node_cpu_usage_ratio", "cluster_pod_usage_ratio", "cluster_memory_usage_ratio", "cluster_cpu_usage_ratio"
		if strings.ToLower(r.Name) == strings.ToLower("node_pod_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("node_memory_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("node_cpu_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "node": p["node"], "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("cluster_pod_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("cluster_memory_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "value": p["value"]})
		}
		if strings.ToLower(r.Name) == strings.ToLower("cluster_cpu_usage_ratio") {
			p := r.ParseString()
			otherMetricData[r.Name] = append(otherMetricData[r.Name], map[string]string{"cluster": result.Spec.InspectCluster.Name, "value": p["value"]})
		}
	}
	return otherMetricData
}

func metricComputed(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b

}

func OverviewCount(metric map[string][]map[string]string, com []kubeeyev1alpha2.ComponentResultItem) map[string]int {
	count := map[string]int{"node_load_15": 0, "filesystem_readonly": 0, "filesystem_avail": 0, "harbor_health": 0, "harbor_ref_work_replication": 0, "node_loadapp_etcd_backup_status": 0}

	for key := range count {
		count[key] = len(metric[key])
	}
	componentCount := 0
	for _, c := range com {
		if c.Assert {
			componentCount += 1
		}
	}
	count["component"] = componentCount
	return count
}
