package cli

import (
	"github.com/xortim/snapback/internal/config"
	"github.com/xortim/snapback/internal/retention"
)

// retentionPolicy converts the config's retention block to the policy
// internal/retention evaluates. Shared by run and prune.
func retentionPolicy(r config.Retention) retention.Policy {
	return retention.Policy{KeepLast: r.KeepLast, KeepDaily: r.KeepDaily, KeepWeekly: r.KeepWeekly}
}
