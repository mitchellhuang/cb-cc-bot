package email

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// amountRe matches "Your automatic payment of $6,840.03 is scheduled for..."
var amountRe = regexp.MustCompile(`Your automatic payment of \$([\d,]+\.\d{2})`)

// ParsePaymentAmount extracts the USD payment amount from a Coinbase autopay reminder email body.
func ParsePaymentAmount(body string) (float64, error) {
	matches := amountRe.FindStringSubmatch(body)
	if len(matches) < 2 {
		return 0, fmt.Errorf("no payment amount found in email body")
	}
	s := strings.ReplaceAll(matches[1], ",", "")
	amount, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse amount %q: %w", s, err)
	}
	return amount, nil
}
