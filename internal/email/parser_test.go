package email

import "testing"

func TestParsePaymentAmount(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    float64
		wantErr bool
	}{
		{
			name: "real email format",
			body: "Your automatic payment of $6,840.03 is scheduled for April 4",
			want: 6840.03,
		},
		{
			name: "no comma",
			body: "Your automatic payment of $999.00 is scheduled for May 1",
			want: 999.00,
		},
		{
			name: "small amount",
			body: "Your automatic payment of $123.45 is scheduled for April 15",
			want: 123.45,
		},
		{
			name: "surrounded by other text",
			body: "Hi,\n\nYour automatic payment of $1,234.56 is scheduled for April 20.\n\nThanks,\nCoinbase",
			want: 1234.56,
		},
		{
			name:    "no match",
			body:    "Some unrelated email with no payment amount",
			wantErr: true,
		},
		{
			name:    "wrong pattern",
			body:    "Your payment of $100.00 is due", // missing "automatic"
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePaymentAmount(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePaymentAmount() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParsePaymentAmount() = %v, want %v", got, tt.want)
			}
		})
	}
}
