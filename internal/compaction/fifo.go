package compaction

// FIFO drops the oldest messages until the total token count is within
// budget. The newest (last) message is always retained.
type FIFO struct{}

// Compile-time check.
var _ Strategy = (*FIFO)(nil)

// Compact evicts from the front of the slice until the sum of remaining
// tokens is <= budget. At least one message is always kept.
func (FIFO) Compact(msgs []Message, budget int) []Message {
	if len(msgs) == 0 {
		return nil
	}

	total := 0
	for _, m := range msgs {
		total += m.Tokens
	}

	// Already within budget — nothing to do.
	if total <= budget {
		return msgs
	}

	// Walk from the oldest (index 0) forward, subtracting tokens until
	// we can afford the rest.
	drop := 0
	for drop < len(msgs)-1 && total > budget {
		total -= msgs[drop].Tokens
		drop++
	}

	return msgs[drop:]
}
