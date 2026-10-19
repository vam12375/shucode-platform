package store

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

// 人工额度申请是待审核授信，不能表示支付完成或真实收款。
type QuotaRequestRecord struct {
	ID            string     `json:"id"`
	TeamID        string     `json:"teamId"`
	RequestedBy   string     `json:"requestedBy"`
	AmountCredits string     `json:"amountCredits"`
	Reason        string     `json:"reason"`
	Status        string     `json:"status"`
	ReviewedBy    string     `json:"reviewedBy,omitempty"`
	ReviewNote    string     `json:"reviewNote"`
	CreatedAt     time.Time  `json:"createdAt"`
	ReviewedAt    *time.Time `json:"reviewedAt,omitempty"`
}

var (
	ErrQuotaRequestReviewed = errors.New("额度申请已审核，不能重复处理")
	ErrQuotaRequestInvalid  = errors.New("额度申请无效或团队不可用")
)

const maxQuotaRequestMicro int64 = 999_999_999_999_999_999

// 金额不能借用浮点解析：舍入和整数乘法溢出会破坏审批金额与 numeric(18,6) 上限。
func parseQuotaRequestMicro(value string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) > 2 || len(parts[0]) < 1 || len(parts[0]) > 12 {
		return 0, ErrQuotaRequestInvalid
	}
	for _, part := range parts {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, ErrQuotaRequestInvalid
			}
		}
	}
	if len(parts) == 2 && (len(parts[1]) < 1 || len(parts[1]) > 6) {
		return 0, ErrQuotaRequestInvalid
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, ErrQuotaRequestInvalid
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10, 64)
		if err != nil {
			return 0, ErrQuotaRequestInvalid
		}
	}
	return whole*1_000_000 + fraction, nil
}

func NormalizeQuotaRequestAmount(value string) (string, error) {
	micro, err := parseQuotaRequestMicro(value)
	if err != nil || micro <= 0 {
		return "", ErrQuotaRequestInvalid
	}
	return credits.FormatMicroCredits(micro), nil
}

func normalizeQuotaRequest(record QuotaRequestRecord) (QuotaRequestRecord, error) {
	amount, err := NormalizeQuotaRequestAmount(record.AmountCredits)
	if err != nil {
		return record, err
	}
	record.AmountCredits = amount
	record.Reason = strings.TrimSpace(record.Reason)
	if utf8.RuneCountInString(record.Reason) < 1 || utf8.RuneCountInString(record.Reason) > 500 || record.RequestedBy == "" || record.TeamID == "" || (record.Status != "" && record.Status != "pending") || record.ReviewedBy != "" || record.ReviewedAt != nil {
		return record, ErrQuotaRequestInvalid
	}
	record.Status = "pending"
	record.ReviewNote = ""
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	return record, nil
}

func cloneQuotaRequest(record QuotaRequestRecord) QuotaRequestRecord {
	record.ReviewedAt = copyTime(record.ReviewedAt)
	return record
}
func validQuotaReview(decision, note string) bool {
	return (decision == "approve" || decision == "reject") && utf8.RuneCountInString(note) <= 500
}
