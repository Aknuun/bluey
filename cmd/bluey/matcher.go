package main

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

type Transaction struct {
	Key         string
	Description string
	Date        string
	AmountRial  int64
	Incoming    bool
}

var amountLineRe = regexp.MustCompile(`^([۰-۹0-9٠-٩٬,]+)\s*(ریال|تومان)$`)

func parseTransactions(text string) []Transaction {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var out []Transaction
	counts := make(map[string]int)
	for i, line := range lines {
		m := amountLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		end := i - 1
		for end >= 0 && strings.TrimSpace(lines[end]) == "" {
			end--
		}
		start := end
		for start >= 0 && strings.TrimSpace(lines[start]) != "" {
			start--
		}
		start++
		if start > end {
			continue
		}
		desc := strings.TrimSpace(lines[start])
		date := strings.TrimSpace(lines[end])
		if desc == "موجودی" || amountLineRe.MatchString(desc) {
			continue
		}
		amount := parseAmount(m[1])
		if amount <= 0 {
			continue
		}
		if m[2] == "تومان" {
			amount *= 10
		}
		base := desc + "|" + date + "|" + strconv.FormatInt(amount, 10)
		counts[base]++
		key := sha1Hex(base + "|" + strconv.Itoa(counts[base]))
		out = append(out, Transaction{
			Key:         key,
			Description: desc,
			Date:        date,
			AmountRial:  amount,
			Incoming:    isIncoming(desc),
		})
	}
	return out
}

func parseAmount(s string) int64 {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	n, _ := strconv.ParseInt(b.String(), 10, 64)
	return n
}

func isIncoming(desc string) bool {
	return strings.HasPrefix(desc, "دریافت") || strings.Contains(desc, "واریز") || strings.Contains(desc, "شارژ حساب")
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
