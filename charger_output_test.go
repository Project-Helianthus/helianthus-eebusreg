package eebusruntime

func chargerOutputLens(resourceID string) LensResult {
	return LensResult{
		Family: "charger", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true,
		Elements: []string{
			"total active power", "phase active power", "phase current", "phase voltage", "frequency",
			"session energy", "lifetime energy", "operating state", "charging status", "connection status",
			"fault status", "readiness status",
		},
	}
}
