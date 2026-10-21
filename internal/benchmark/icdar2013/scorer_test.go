package icdar2013

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRelationsNormalizeAndPreserveMultiplicity(t *testing.T) {
	table := [][]string{{"  ALPHA\n beta ", "", "Value"}, {"alpha beta", "", "value"}}
	assert.Equal(t, []Relation{
		{Direction: "below", From: "alpha beta", To: "alpha beta", Count: 1},
		{Direction: "below", From: "value", To: "value", Count: 1},
		{Direction: "right", From: "alpha beta", To: "value", Count: 2},
	}, Relations(table))
}

func TestCompareRelationsUsesMultisetIntersection(t *testing.T) {
	expected := [][]string{{"A", "B"}, {"A", "B"}}
	actual := [][]string{{"a", "b"}, {"a", ""}}
	assert.Equal(t, Score{Expected: 4, Actual: 2, Matched: 2, Precision: 1, Recall: 0.5, F1: 2.0 / 3.0},
		CompareDocuments([][][]string{expected}, [][][]string{actual}))
}

func TestRelationsSkipMissingColumnsInJaggedRows(t *testing.T) {
	table := [][]string{{"A", "B"}, {"C"}, {"D", "E"}}
	assert.Equal(t, []Relation{
		{Direction: "below", From: "a", To: "c", Count: 1},
		{Direction: "below", From: "b", To: "e", Count: 1},
		{Direction: "below", From: "c", To: "d", Count: 1},
		{Direction: "right", From: "a", To: "b", Count: 1},
		{Direction: "right", From: "d", To: "e", Count: 1},
	}, Relations(table))
}

func TestValidateScoreRejectsCorruptCountsAndMetrics(t *testing.T) {
	valid := scoreFromCounts(4, 5, 3)
	tests := []struct {
		name   string
		mutate func(*Score)
	}{
		{"negative expected", func(score *Score) { score.Expected = -1 }},
		{"negative actual", func(score *Score) { score.Actual = -1 }},
		{"negative matched", func(score *Score) { score.Matched = -1 }},
		{"matched exceeds expected", func(score *Score) { score.Matched = score.Expected + 1 }},
		{"matched exceeds actual", func(score *Score) { score.Matched = score.Actual + 1 }},
		{"negative precision", func(score *Score) { score.Precision = -0.1 }},
		{"precision above one", func(score *Score) { score.Precision = 1.1 }},
		{"nan recall", func(score *Score) { score.Recall = math.NaN() }},
		{"infinite f1", func(score *Score) { score.F1 = math.Inf(1) }},
		{"inconsistent precision", func(score *Score) { score.Precision += 2 * scoreTolerance }},
		{"inconsistent recall", func(score *Score) { score.Recall -= 2 * scoreTolerance }},
		{"inconsistent f1", func(score *Score) { score.F1 += 2 * scoreTolerance }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := valid
			test.mutate(&score)
			assert.Error(t, validateScore(score))
		})
	}

	for _, score := range []Score{
		scoreFromCounts(0, 0, 0),
		scoreFromCounts(0, 3, 0),
		scoreFromCounts(3, 0, 0),
	} {
		assert.NoError(t, validateScore(score))
	}
	invalidZero := scoreFromCounts(0, 0, 0)
	invalidZero.Precision = 0.1
	assert.Error(t, validateScore(invalidZero))
}
