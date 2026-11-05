package runner

func completionPolicy(strategy string) string {
	if strategy == "strict-greedy" {
		return "top1_only_stop_on_failure"
	}
	return "ranked_complete_candidates_until_public_pass"
}
