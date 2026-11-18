package llm

import (
	"strconv"
	"strings"
	"testing"
)

// TestRepetitionDetector_spanFloor pins the byte floor that keeps the count
// thresholds honest.
//
// The counts are byte-blind: 200 consecutive "0 " tokens is 400 bytes, which a
// legitimate single-column data dump reaches, and 64 repetitions of a 9-byte
// shingle is 576 bytes. Truncating a correct answer is worse than catching a
// short loop late, so a loop must also span repetitionMinSpanBytes.
func TestRepetitionDetector_spanFloor(t *testing.T) {
	t.Run("a short-token column does not trip", func(t *testing.T) {
		// 300 "0 " tokens: past the 200-token bar, only 600 bytes.
		d := repetitionDetector{}
		if _, tripped := feedIn(&d, strings.Repeat("0 ", 300), 0); tripped {
			t.Fatal("a 600-byte run of one short token tripped the guard; a legitimate column of data would be truncated")
		}
	})

	t.Run("the same run trips once it spans the floor", func(t *testing.T) {
		d := repetitionDetector{}
		if _, tripped := feedIn(&d, strings.Repeat("0 ", 1200), 0); !tripped {
			t.Fatal("a 2400-byte run of one short token did not trip; the floor must not disable detection")
		}
	})

	t.Run("the incident trips", func(t *testing.T) {
		// The loop that motivated the guard: "GO" repeated 334,144 times.
		d := repetitionDetector{}
		loop, tripped := feedIn(&d, strings.Repeat("GO ", 5000), 333)
		if !tripped {
			t.Fatal("the GO loop that motivated this guard did not trip")
		}
		if loop.KeepBytes != 0 {
			t.Errorf("kept %d bytes of a loop that starts at 0", loop.KeepBytes)
		}
		if loop.Token != "GO" {
			t.Errorf("token = %q; want %q", loop.Token, "GO")
		}
	})
}

// TestRepetitionDetector_longPeriod pins that the period bound covers a shingle
// a model actually gets stuck on. A 9-token sentence matches no shorter period,
// so a period cap of 8 was a detection cliff: those loops ran unbounded.
func TestRepetitionDetector_longPeriod(t *testing.T) {
	const shingle = "alpha beta gamma delta epsilon zeta eta theta iota "

	d := repetitionDetector{minSpan: 1}
	content := "Here is the plan:\n" + strings.Repeat(shingle, repetitionShingleReps)
	loop, tripped := feedIn(&d, content, 0)
	if !tripped {
		t.Fatalf("a %d-token shingle repeated %d times did not trip", len(strings.Fields(shingle)), repetitionShingleReps)
	}
	if loop.KeepBytes != len("Here is the plan:\n") {
		t.Errorf("KeepBytes = %d; want %d (the preamble only)", loop.KeepBytes, len("Here is the plan:\n"))
	}
	if loop.Period != len(strings.Fields(shingle)) {
		t.Errorf("Period = %d; want %d", loop.Period, len(strings.Fields(shingle)))
	}
}

// TestRepetitionDetector_whitespaceFreeLoop pins the byte-level path. Text with
// no spaces at all never reaches the token ring in one piece — it is cut into
// fixed-size chunks — and a loop whose period does not divide the chunk size
// produces chunks that never match, so the token path sees no repetition at all.
func TestRepetitionDetector_whitespaceFreeLoop(t *testing.T) {
	t.Run("a repeated CJK sentence trips", func(t *testing.T) {
		const sentence = "今日は良い天気です。"
		d := repetitionDetector{}
		content := strings.Repeat(sentence, 400) // ~12 KB, no whitespace
		loop, tripped := feedIn(&d, content, 97)
		if !tripped {
			t.Fatal("a repeated whitespace-free sentence did not trip; the byte-level path is not wired")
		}
		if loop.Count < repetitionShingleReps {
			t.Errorf("Count = %d; want >= %d", loop.Count, repetitionShingleReps)
		}
	})

	t.Run("non-repeating whitespace-free text does not trip", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 4000; i++ {
			b.WriteString(strconv.Itoa(i))
		}
		d := repetitionDetector{}
		if _, tripped := feedIn(&d, b.String(), 101); tripped {
			t.Fatal("a non-repeating digit blob tripped the byte-level path")
		}
	})
}
