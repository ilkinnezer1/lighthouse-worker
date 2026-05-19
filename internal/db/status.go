package db

import dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"

func IsDeploymentTerminal(s dbgen.DeploymentStatus) bool {
	return s == dbgen.DeploymentStatusSUCCEEDED ||
		s == dbgen.DeploymentStatusFAILED ||
		s == dbgen.DeploymentStatusCANCELLED
}
