package server

func (r *QueryResult) Path() string {
	switch {
	case r.CacheHit:
		return "Cache Hit"
	case r.PrefetchWait:
		return "Prefetch Hit"
	case r.LocalAnswer:
		return "Local Answer"
	case normalizeAction(r.Action) == "nxdomain":
		return "NXDOMAIN RULE"
	case normalizeAction(r.Action) == "zeroip":
		return "ZERO-IP RULE"
	default:
		return "Upstream Query"
	}
}
