package model

// NodeRuntimePolicy is enforced by the data plane. Zero limits mean unlimited.
type NodeRuntimePolicy struct {
	LimitTotalGB  float64          `json:"limitTotalGb"`
	LimitKind     string           `json:"limitKind"`
	LimitResetDay int              `json:"limitResetDay"`
	RevokedBefore map[string]int64 `json:"revokedBefore,omitempty"`
	HTTPPort      int              `json:"httpPort"`
	ForwardPorts  string           `json:"forwardPorts"`
	LimiterMbps   int              `json:"limiterMbps"`
	Concurrency   int              `json:"concurrency"`
	Connections   int              `json:"connections"`
	MaxPoolCount  int              `json:"maxPoolCount"`
}

type ForwardTrafficDay struct {
	Day           string `json:"day"`
	UploadBytes   int64  `json:"uploadBytes"`
	DownloadBytes int64  `json:"downloadBytes"`
}
