package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const noiseCSVHeaderMarker = "Text Collection To Clean"

var (
	patentNoiseMu sync.RWMutex
	patentNoiseRE *regexp.Regexp
)

// InitializePatentNoiseCleaner reads noise phrases from csvPath once, builds a single
// case-insensitive alternation regex (longest phrases first), and stores it for
// cleanPatentInventionNoise. Fails if the file cannot be read, no non-empty phrases
// are loaded, or the regex cannot be compiled.
func InitializePatentNoiseCleaner(csvPath string) error {
	phrases, err := loadNoisePhrasesFromCSV(csvPath)
	if err != nil {
		return err
	}
	if len(phrases) == 0 {
		return fmt.Errorf("patent noise CSV %q: no phrases loaded", csvPath)
	}
	re, err := compilePatentNoiseRegex(phrases)
	if err != nil {
		return fmt.Errorf("patent noise regex: %w", err)
	}
	patentNoiseMu.Lock()
	patentNoiseRE = re
	patentNoiseMu.Unlock()
	return nil
}

func loadNoisePhrasesFromCSV(csvPath string) ([]string, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open patent noise CSV: %w", err)
	}
	defer f.Close()
	return readNoisePhrasesFromCSV(f)
}

func readNoisePhrasesFromCSV(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	seen := make(map[string]struct{})
	var phrases []string
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV row: %w", err)
		}
		if len(rec) == 0 {
			continue
		}
		p := strings.TrimSpace(rec[0])
		p = strings.ToValidUTF8(p, "")
		if p == "" {
			continue
		}
		if strings.EqualFold(p, noiseCSVHeaderMarker) {
			continue
		}
		key := strings.ToLower(p)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		phrases = append(phrases, p)
	}
	return phrases, nil
}

func compilePatentNoiseRegex(phrases []string) (*regexp.Regexp, error) {
	if len(phrases) == 0 {
		return nil, fmt.Errorf("empty phrase set")
	}
	sort.Slice(phrases, func(i, j int) bool {
		if len(phrases[i]) != len(phrases[j]) {
			return len(phrases[i]) > len(phrases[j])
		}
		return phrases[i] < phrases[j]
	})
	var b strings.Builder
	b.Grow(len(phrases) * 32)
	b.WriteString("(?i)")
	for i, p := range phrases {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(regexp.QuoteMeta(p))
	}
	return regexp.Compile(b.String())
}

func cleanPatentInventionNoise(s string) string {
	patentNoiseMu.RLock()
	re := patentNoiseRE
	patentNoiseMu.RUnlock()
	if re == nil {
		panic("services: patent noise cleaner not initialized (call InitializePatentNoiseCleaner at startup)")
	}
	return cleanPatentInventionNoiseWith(s, re)
}

func cleanPatentInventionNoiseWith(s string, re *regexp.Regexp) string {
	out := re.ReplaceAllString(s, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(out), " "))
}
