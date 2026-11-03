package irreversibleguard

import (
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

func TestGcloudDeletePermissionIsData(t *testing.T) {
	check(t, "", hooktest.Commands(
		"gcloud compute instances test-iam-permissions example-instance --permissions=compute.instances.delete",
		"gcloud compute instances test-iam-permissions example-instance --permissions 'compute.instances.delete'",
		"/usr/local/bin/gcloud compute instances test-iam-permissions example-instance \\\n  --permissions=compute.instances.delete --format=json",
		// 履歴の projects test-iam-permissions は現行 CLI に無いが、削除操作でもない。
		"gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete,container.jobs.get",
	))
}

func TestGcloudDeleteStillBlocked(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gcloud compute instances delete example-instance", Label: labelGcloudDelete},
		{Command: "gcloud compute instances 'delete' example-instance", Label: labelGcloudDelete},
		{Command: `gcloud compute instances "delete" example-instance`, Label: labelGcloudDelete},
		{Command: "gcloud alpha compute instances delete example-instance", Label: labelGcloudDelete},
		{Command: "gcloud storage rsync source gs://example-bucket --delete-unmatched-destination-objects", Label: labelGcloudDelete},
		{Command: "/usr/local/bin/gcloud compute instances delete example-instance --permissions=compute.instances.delete", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete; gcloud projects delete example-project", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete\ngcloud projects delete example-project", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete && gh auth logout", Label: labelGHLogout},
		{Command: "gcloud auth revoke --permissions=container.jobs.delete", Label: labelGcloudRevoke},
	})
}

// 判定できない形を例外にすると、展開後のサブコマンドや別の実行を見逃すため、従来の拒否を残す。
func TestGcloudUncertainArgumentsKeepOriginalDecision(t *testing.T) {
	check(t, hooktest.Deny, []hooktest.Case{
		{Command: "gcloud compute instances $operation example-instance --permissions=compute.instances.delete", Label: labelGcloudDelete},
		{Command: "gcloud compute instances $(echo delete) example-instance", Label: labelGcloudDelete},
		{Command: "gcloud compute instances `echo delete` example-instance", Label: labelGcloudDelete},
		{Command: "bash -c 'echo before; gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete'", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete --flags-file=flags.yaml", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete --flags-file flags.yaml", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions=container.jobs.delete > .env", Label: labelGcloudDelete},
		{Command: "gcloud projects test-iam-permissions example-project --permissions='container.jobs.delete", Label: labelGcloudDelete},
	})
}
