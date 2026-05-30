package main

// runnerResult is the JSON protocol emitted by bplay-runner. Keep it small:
// bplayd maps this into the public /api/run step shape.
type runnerResult struct {
	ExitCode  int    `json:"exitCode"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	MS        int64  `json:"ms"`
	TimedOut  bool   `json:"timedOut"`
	Killed    bool   `json:"killed"`
	Reason    string `json:"reason,omitempty"`
	Truncated bool   `json:"truncated"`
}
