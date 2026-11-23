package mcpserver

import (
	"encoding/json"
	"strings"
)

// The input schemas of DESIGN.md §5, written out so the published contract is
// exactly the document's; the shared types ($defs in the TD) are spliced in.
const (
	defDate   = `{"type": "string", "format": "date", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "YYYY-MM-DD"}`
	defAmount = `{"type": "integer", "minimum": 0, "maximum": 1000000000000000, "description": "Đồng Việt Nam, số nguyên"}`
	defPeriod = `{"type": "object", "additionalProperties": false, "required": ["kind", "year"],
	  "properties": {
	    "kind": {"type": "string", "enum": ["month", "quarter", "year"]},
	    "year": {"type": "integer", "minimum": 2020, "maximum": 2100},
	    "number": {"type": "integer", "minimum": 1, "maximum": 12, "description": "tháng 1–12 hoặc quý 1–4; bỏ trống với năm"}}}`
	defRevenueByGroup = `{"type": "array", "minItems": 1, "maxItems": 10,
	  "items": {"type": "object", "additionalProperties": false, "required": ["activity_group", "revenue_minor"],
	    "properties": {"activity_group": {"type": "string", "maxLength": 64}, "revenue_minor": $amount}}}`
	defIllustrative = `{"type": "boolean", "default": false, "description": "true = chấp nhận kết quả minh họa khi bộ quy tắc chưa được xác minh"}`
)

var schemaSources = map[string]string{
	"tra_cuu_van_ban": `{"type": "object", "additionalProperties": false, "required": ["query"],
	  "properties": {
	    "query": {"type": "string", "minLength": 2, "maxLength": 200},
	    "as_of": $date,
	    "doc": {"type": "string", "maxLength": 64, "description": "mã văn bản trong kho (vd nd-254-2026) hoặc số hiệu (vd 254/2026/NĐ-CP)"},
	    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "default": 5}}}`,
	"doc_dieu_khoan": `{"type": "object", "additionalProperties": false, "required": ["doc", "dieu"],
	  "properties": {
	    "doc": {"type": "string", "maxLength": 64},
	    "dieu": {"type": "integer", "minimum": 1, "maximum": 1000},
	    "khoan": {"type": "integer", "minimum": 1, "maximum": 200},
	    "diem": {"type": "string", "pattern": "^[a-zđ]$"},
	    "as_of": $date}}`,
	"van_ban_ap_dung": `{"type": "object", "additionalProperties": false, "required": ["topic", "as_of"],
	  "properties": {
	    "topic": {"type": "string", "enum": ["hoa_don", "quan_ly_thue", "gtgt", "hkd", "ke_toan"]},
	    "as_of": $date}}`,
	"tinh_thue_hkd": `{"type": "object", "additionalProperties": false, "required": ["period", "revenue_by_group"],
	  "properties": {"period": $period, "revenue_by_group": $revenue, "illustrative": $illustrative}}`,
	"tinh_thue_gtgt": `{"type": "object", "additionalProperties": false, "required": ["method", "period"],
	  "properties": {
	    "method": {"type": "string", "enum": ["khau_tru", "truc_tiep"]},
	    "period": $period,
	    "khau_tru": {"type": "object", "additionalProperties": false,
	      "required": ["output_vat_minor", "input_vat_deductible_minor"],
	      "properties": {"output_vat_minor": $amount, "input_vat_deductible_minor": $amount, "carried_forward_minor": $amount}},
	    "truc_tiep": {"type": "object", "additionalProperties": false, "required": ["revenue_by_group"],
	      "properties": {"revenue_by_group": $revenue}},
	    "illustrative": $illustrative}}`,
	"han_nop": `{"type": "object", "additionalProperties": false, "required": ["entity_type", "tax", "period"],
	  "properties": {
	    "entity_type": {"type": "string", "enum": ["dn", "hkd"]},
	    "tax": {"type": "string", "enum": ["gtgt", "tncn", "hkd"]},
	    "method": {"type": "string", "enum": ["khau_tru", "truc_tiep"]},
	    "period": $period,
	    "illustrative": $illustrative}}`,
	"kiem_tra_but_toan": `{"type": "object", "additionalProperties": false,
	  "required": ["regime", "transaction_date", "document", "draft_lines"],
	  "properties": {
	    "regime": {"type": "string", "enum": ["TT133"]},
	    "transaction_date": $date,
	    "document": {"type": "object", "additionalProperties": false, "required": ["kind", "direction"],
	      "properties": {
	        "kind": {"type": "string", "enum": ["invoice"]},
	        "direction": {"type": "string", "enum": ["input", "output"]},
	        "vat_rate": {"type": "string", "maxLength": 8},
	        "subtotal_minor": $amount, "vat_minor": $amount, "total_minor": $amount,
	        "counterpart_account": {"type": "string", "pattern": "^[0-9]{3,6}$"}}},
	    "draft_lines": {"type": "array", "minItems": 1, "maxItems": 50,
	      "items": {"type": "object", "additionalProperties": false, "required": ["account_code"],
	        "properties": {"account_code": {"type": "string", "pattern": "^[0-9]{3,6}$"},
	                       "debit_minor": $amount, "credit_minor": $amount}}},
	    "illustrative": $illustrative}}`,
	"kiem_tra_hoa_don": `{"type": "object", "additionalProperties": false, "required": ["issue_date", "draft"],
	  "properties": {
	    "issue_date": $date,
	    "buyer_kind": {"type": "string", "enum": ["business", "individual"]},
	    "draft": {"type": "object", "description": "hóa đơn nháp theo dạng chuẩn: form, series, number, issued_on, currency, seller {name, mst, address}, buyer {name, mst, address}, lines [{name, unit, quantity, unit_price, amount_minor, vat_rate, vat_minor}], vat_rates, vat_minor, total_minor; khóa lạ được bỏ qua"},
	    "illustrative": $illustrative}}`,
}

// inputSchema returns a tool's schema as JSON with the shared types spliced in.
func inputSchema(tool string) json.RawMessage {
	r := strings.NewReplacer(
		"$date", defDate, "$amount", defAmount, "$period", defPeriod,
		"$revenue", strings.ReplaceAll(defRevenueByGroup, "$amount", defAmount), "$illustrative", defIllustrative,
	)
	return json.RawMessage(r.Replace(schemaSources[tool]))
}
