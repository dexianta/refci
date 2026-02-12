package tui

func renderHint(key, desc string) string {
	return keycapStyle.Render(key) + " " + desc
}

func modIdx(idx, mod, delta int) int {
	if mod == 0 {
		return 0
	}
	if delta > 0 {
		delta = 1
	}
	if delta < 0 {
		delta = -1
	}

	if idx+delta >= mod {
		return (idx + delta) % mod
	}

	if idx+delta < 0 {
		//
		return mod + delta
	}
	return idx + delta
}
