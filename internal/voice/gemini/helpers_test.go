package gemini

// countSent counts the content messages carrying text.
func countSent(c *fakeConn, text string) int {
	n := 0
	for _, m := range c.sentMsgs() {
		if isContent(text)(m) {
			n++
		}
	}
	return n
}
