package models

// Resolve returns a model id for every stage and the selections it dropped,
// each as "stage=value".
func Resolve(c Catalog, s Selections) (map[Stage]string, []string) {
	out := make(map[Stage]string, len(Stages))
	if !c.SupportsSelection {
		for _, st := range Stages {
			out[st] = ""
		}
		return out, nil
	}
	var dropped []string
	seen := map[string]bool{}
	for _, st := range Stages {
		raw, key := s[string(st)], string(st)
		if raw == "" {
			raw, key = s[DefaultKey], DefaultKey
		}
		def := c.ForTier(c.StageDefaults[st])
		if raw == "" {
			out[st] = def
			continue
		}
		switch t, isTier := TierRef(raw); {
		case isTier && c.DeclaresTier(t):
			out[st] = c.ForTier(t)
			continue
		case !isTier:
			if _, ok := c.Lookup(raw); ok {
				out[st] = raw
				continue
			}
		}
		out[st] = def
		if entry := key + "=" + raw; !seen[entry] {
			seen[entry] = true
			dropped = append(dropped, entry)
		}
	}
	return out, dropped
}
