package dispatch

// dotBaseIdentBefore returns the identifier immediately before a
// `.` at refStart, or "" if the preceding char isn't a dot or no
// identifier precedes. Shared by language-specific cross-file
// handlers for receiver-type disambiguation.
func dotBaseIdentBefore(src []byte, refStart uint32) string {
	if int(refStart) <= 0 || int(refStart) > len(src) {
		return ""
	}
	i := int(refStart) - 1
	if src[i] != '.' {
		return ""
	}
	end := i
	i--
	for i >= 0 {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i--
			continue
		}
		break
	}
	return string(src[i+1 : end])
}
