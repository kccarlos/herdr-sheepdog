package wav

// Bytes returns how many PCM bytes were written so far.
func (wr *Writer) Bytes() int64 { return wr.n }
