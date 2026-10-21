// Package icdar2013 provides deterministic relation scoring for the externally
// supplied ICDAR 2013 table-recognition corpus.
package icdar2013

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	NormalizationMethod = "unicode-lower-whitespace-collapse-v1"
	ScoringMethod       = "nearest-nonempty-right-below-multiset-v1"
	scoreTolerance      = 1e-12
)

// Relation records the nearest non-empty cell to the right or below a cell.
// Count preserves multiplicity when equal normalized values occur repeatedly.
type Relation struct {
	Direction string `json:"direction"`
	From      string `json:"from"`
	To        string `json:"to"`
	Count     int    `json:"count"`
}

// Score summarizes multiset relation agreement.
type Score struct {
	Expected  int     `json:"expected"`
	Actual    int     `json:"actual"`
	Matched   int     `json:"matched"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

type relationKey struct {
	direction string
	from      string
	to        string
}

// NormalizeCell applies only case folding and whitespace normalization.
func NormalizeCell(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// Relations derives nearest non-empty right/below relations with multiplicity.
func Relations(table [][]string) []Relation {
	counts := make(map[relationKey]int)
	for row, cells := range table {
		for column, raw := range cells {
			from := NormalizeCell(raw)
			if from == "" {
				continue
			}
			for right := column + 1; right < len(cells); right++ {
				if to := NormalizeCell(cells[right]); to != "" {
					counts[relationKey{direction: "right", from: from, to: to}]++
					break
				}
			}
			for below := row + 1; below < len(table); below++ {
				if column >= len(table[below]) {
					continue
				}
				if to := NormalizeCell(table[below][column]); to != "" {
					counts[relationKey{direction: "below", from: from, to: to}]++
					break
				}
			}
		}
	}
	result := make([]Relation, 0, len(counts))
	for key, count := range counts {
		result = append(result, Relation{Direction: key.direction, From: key.from, To: key.to, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Direction != result[j].Direction {
			return result[i].Direction < result[j].Direction
		}
		if result[i].From != result[j].From {
			return result[i].From < result[j].From
		}
		return result[i].To < result[j].To
	})
	return result
}

func relationCounts(relations []Relation) map[relationKey]int {
	counts := make(map[relationKey]int, len(relations))
	for index := range relations {
		relation := &relations[index]
		counts[relationKey{direction: relation.Direction, from: relation.From, to: relation.To}] += relation.Count
	}
	return counts
}

func validateScore(score Score) error {
	if score.Expected < 0 || score.Actual < 0 || score.Matched < 0 {
		return fmt.Errorf("counts must be nonnegative")
	}
	if score.Matched > score.Expected || score.Matched > score.Actual {
		return fmt.Errorf("matched count exceeds expected or actual count")
	}
	expected := scoreFromCounts(score.Expected, score.Actual, score.Matched)
	for _, value := range []struct {
		name         string
		actual, want float64
	}{
		{"precision", score.Precision, expected.Precision},
		{"recall", score.Recall, expected.Recall},
		{"f1", score.F1, expected.F1},
	} {
		if math.IsNaN(value.actual) || math.IsInf(value.actual, 0) || value.actual < 0 || value.actual > 1 {
			return fmt.Errorf("%s must be finite and between zero and one", value.name)
		}
		// Reports are JSON files. This tolerance permits normal decimal
		// round trips without accepting hand-edited metrics.
		if math.Abs(value.actual-value.want) > scoreTolerance {
			return fmt.Errorf("%s does not match score counts", value.name)
		}
	}
	return nil
}

func scoreFromCounts(expected, actual, matched int) Score {
	score := Score{Expected: expected, Actual: actual, Matched: matched}
	if actual > 0 {
		score.Precision = float64(matched) / float64(actual)
	}
	if expected > 0 {
		score.Recall = float64(matched) / float64(expected)
	}
	if score.Precision+score.Recall > 0 {
		score.F1 = 2 * score.Precision * score.Recall / (score.Precision + score.Recall)
	}
	return score
}
