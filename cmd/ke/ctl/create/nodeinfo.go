package create

import (
	"fmt"
	"github.com/kubesphere/kubeeye/pkg/constant"
	"github.com/kubesphere/kubeeye/pkg/inspect"
	"github.com/kubesphere/kubeeye/pkg/kube"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
	"os"
)

func NewNodeInfoCmd(client *kube.KubernetesClient) *cobra.Command {
	sysctlCmd := &cobra.Command{
		Use:   constant.NodeInfo,
		Short: "inspect on nodeinfo rule on Kubernetes cluster.",
		Run: func(cmd *cobra.Command, args []string) {

			if len(taskName) == 0 || len(resultName) == 0 {
				klog.Errorf("taskName  or resultName Incomplete parameters")
				os.Exit(1)
			}

			err := inspect.JobInspect(cmd.Context(), taskName, resultName, client, constant.NodeInfo)
			if err != nil {
				klog.Errorf("kubeeye inspect failed with error: %s,%v", err, err)
				os.Exit(1)
			}
			fmt.Println(args, taskName, "inspect success")
		},
	}
	return sysctlCmd
}