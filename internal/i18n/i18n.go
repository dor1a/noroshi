// Package i18n holds the UI message catalogs and the localized text type
// used in the YAML config.
package i18n

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Supported lists the UI languages. English is the default; Korean is a
// full translation.
var Supported = []string{"en", "ko"}

// Text is a string per locale. In YAML it can be a plain string, which then
// applies to every locale, or a mapping of locale to string.
type Text map[string]string

// UnmarshalYAML accepts either a scalar or a mapping.
func (t *Text) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*t = Text{"*": n.Value}
		return nil
	case yaml.MappingNode:
		m := map[string]string{}
		if err := n.Decode(&m); err != nil {
			return err
		}
		for k := range m {
			if Normalize(k) == "" {
				return fmt.Errorf("line %d: unsupported locale %q", n.Line, k)
			}
		}
		*t = m
		return nil
	default:
		return fmt.Errorf("line %d: expected a string or a locale mapping", n.Line)
	}
}

// Get returns the text for a locale, falling back to English, Korean, and
// then any value.
func (t Text) Get(locale string) string {
	if v := t["*"]; v != "" {
		return v
	}
	if v := t[locale]; v != "" {
		return v
	}
	for _, l := range []string{"en", "ko"} {
		if v := t[l]; v != "" {
			return v
		}
	}
	for _, v := range t {
		if v != "" {
			return v
		}
	}
	return ""
}

// Normalize maps "ko-KR" or "EN" to a supported locale, or "" if unsupported.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	for _, l := range Supported {
		if s == l {
			return l
		}
	}
	return ""
}

// FromAcceptLanguage picks the first supported locale from the header,
// ignoring quality values, which browsers already send in order.
func FromAcceptLanguage(h string) string {
	for _, part := range strings.Split(h, ",") {
		tag := part
		if i := strings.IndexByte(tag, ';'); i >= 0 {
			tag = tag[:i]
		}
		if l := Normalize(tag); l != "" {
			return l
		}
	}
	return ""
}

// T returns a UI message, falling back to English and then the key itself.
func T(locale, key string) string {
	if v, ok := messages[locale][key]; ok {
		return v
	}
	if v, ok := messages["en"][key]; ok {
		return v
	}
	return key
}

var messages = map[string]map[string]string{
	"ko": {
		"status.operational":          "정상",
		"status.degraded_performance": "성능 저하",
		"status.partial_outage":       "부분 장애",
		"status.major_outage":         "주요 장애",
		"status.under_maintenance":    "점검 중",
		"status.unknown":              "데이터 없음",
		"check.type.http":             "HTTP 요청",
		"check.type.tcp":              "TCP 연결",
		"check.type.tls":              "TLS 인증서",
		"check.type.domain_expiry":    "도메인 만료",
		"check.type.docker":           "컨테이너",

		"banner.operational":          "모든 시스템 정상",
		"banner.degraded_performance": "일부 시스템 성능 저하",
		"banner.partial_outage":       "일부 시스템 장애",
		"banner.major_outage":         "주요 장애 발생",
		"banner.under_maintenance":    "점검 진행 중",
		"banner.unknown":              "상태 확인 중",

		"uptime.window":  "최근 %d일",
		"uptime.percent": "가용률 %s%%",
		"uptime.nodata":  "데이터 없음",
		"uptime.ago":     "%d일 전",
		"uptime.today":   "오늘",
		"uptime.ongoing": "진행 중",
		"uptime.more":    "외 %d건",
		"uptime.related": "관련 인시던트",

		"page.updated":                 "마지막 갱신",
		"card.title":                   "시스템 상태",
		"verdict.operational":          "현재 확인된 문제가 없습니다.",
		"verdict.degraded_performance": "일부 시스템이 느립니다. 아래에서 영향받는 항목을 볼 수 있습니다.",
		"verdict.partial_outage":       "일부 시스템에 장애가 있습니다. 아래에서 영향받는 항목을 볼 수 있습니다.",
		"verdict.major_outage":         "장애가 발생했습니다. 아래에서 영향받는 항목을 볼 수 있습니다.",
		"verdict.under_maintenance":    "점검이 진행 중입니다.",
		"verdict.unknown":              "체크를 시작하는 중입니다. 곧 상태가 표시됩니다.",
		"group.count":                  "%d/%d 정상",
		"page.list":                    "상태 페이지",
		"page.notfound":                "페이지를 찾을 수 없습니다",
		"page.powered":                 "Powered by noroshi",
		"page.source":                  "GitHub 저장소",
		"lang.switch":                  "English",
		"mode.to_light":                "라이트 모드로 전환",
		"mode.to_dark":                 "다크 모드로 전환",
		"lang.switch_to":               "en",

		"admin.badge":            "관리",
		"admin.checks":           "체크",
		"admin.component":        "컴포넌트",
		"admin.check":            "체크",
		"admin.type":             "종류",
		"admin.target":           "대상",
		"admin.state":            "상태",
		"admin.since":            "변경 시각",
		"admin.last_run":         "마지막 실행",
		"admin.latency":          "응답 시간",
		"admin.detail":           "결과",
		"admin.expiry":           "만료",
		"admin.results":          "실행 결과 (%s)",
		"admin.truncated":        "%[1]d개 중 최신 %[2]d개",
		"admin.no_results_range": "이 기간에 결과가 없습니다",
		"admin.range":            "기간",
		"admin.range_from":       "시작",
		"admin.range_to":         "끝",
		"admin.range_last":       "최근 %s",
		"admin.range_1h":         "1시간",
		"admin.range_6h":         "6시간",
		"admin.range_24h":        "24시간",
		"admin.range_7d":         "7일",
		"admin.range_14d":        "14일",
		"admin.range_30d":        "30일",
		"admin.range_90d":        "90일",
		"admin.range_all":        "전체 %d일",
		"admin.failed_only":      "실패만",
		"admin.latency_title":    "응답 시간 (%s)",
		"admin.latency_chart":    "기간 동안의 응답 시간",
		"admin.lat_avg":          "평균",
		"admin.lat_max":          "최대",
		"admin.lat_fails":        "실패 %d/%d",
		"admin.lat_legend":       "진한 선은 성공한 결과의 평균, 옅은 선은 최대 응답 시간입니다. 빨간 칸은 실패가 있던 구간, 점선은 주의와 위험 기준입니다. 막대에 마우스를 올리면 구간별 값이 보입니다.",
		"admin.show":             "보기",
		"admin.err_range":        "기간을 읽을 수 없어 기본 기간을 보여 줍니다",
		"admin.failures":         "실패 이력 (%s)",
		"admin.fail_start":       "첫 실패",
		"admin.fail_end":         "복구",
		"admin.duration":         "길이",
		"admin.fail_count":       "실패 횟수",
		"admin.judged":           "장애 판정",
		"admin.blip":             "일시 실패",
		"admin.failing":          "실패 중",
		"admin.no_failures":      "이 기간에 실패가 없습니다",
		"admin.streak":           "연속",
		"admin.back":             "목록으로",
		"admin.logout":           "로그아웃",
		"admin.signed_out_title": "로그아웃했습니다",
		"admin.signed_out_body":  "noroshi 관리 화면의 세션을 지웠습니다. 다른 서비스의 통합 로그인은 그대로입니다.",
		"admin.sign_in_again":    "다시 로그인",
		"admin.signed_in":        "로그인",
		"admin.prober":           "prober",
		"admin.prober_seen":      "마지막 활동",
		"admin.prober_none":      "아직 실행 기록이 없습니다",
		"admin.prober_stopped":   "실행 중인 prober가 없습니다",
		"admin.this_leader":      "이 인스턴스가 리더",
		"admin.other_leader":     "다른 인스턴스가 리더",
		"admin.no_results":       "아직 결과가 없습니다",
		"admin.days_left":        "%d일 남음",
		"admin.expired":          "만료됨",
		"admin.dev_mode":         "개발 모드: 인증이 꺼져 있습니다",
		"admin.streak_ok":        "연속 성공 %d회",
		"admin.streak_fail":      "연속 실패 %d회",
		"admin.interval":         "주기",
		"admin.timeout":          "타임아웃",
		"admin.latency_limits":   "응답 시간 기준",
		"admin.latency_tip":      "주황 %s 초과, 빨강 %s 이상",
		"admin.thresholds":       "장애/회복 기준",
		"admin.thresh_fmt":       "연속 실패 %d회 / 연속 성공 %d회",
		"admin.page":             "페이지",
		"admin.cert":             "인증서",
		"admin.domain":           "도메인",
		"admin.skipped":          "판단 보류",
		"nav.home":               "처음으로",

		"state.up":      "정상",
		"state.down":    "장애",
		"state.unknown": "확인 중",
		"state.slow":    "성능 저하",

		"error.forbidden":    "관리자 그룹에 속한 계정만 들어올 수 있습니다",
		"error.login_failed": "로그인에 실패했습니다. 다시 시도해 주세요",
		"error.oidc_down":    "로그인 서버에 연결할 수 없습니다",

		"page.private":                  "비공개",
		"page.public":                   "공개",
		"page.doc":                      "서비스 상태",
		"page.refresh_in":               "%s 후 새로고침",
		"admin.nav_pages":               "상태 페이지",
		"admin.nav_label":               "관리 메뉴",
		"incident.stage.investigating":  "조사 중",
		"incident.stage.identified":     "원인 확인",
		"incident.stage.monitoring":     "지켜보는 중",
		"incident.stage.resolved":       "해결됨",
		"incident.stage.scheduled":      "점검 예정",
		"incident.stage.maint_upcoming": "점검 예정",
		"incident.stage.maint_active":   "점검 중",
		"incident.stage.maint_done":     "점검 완료",
		"maint.upcoming":                "예정된 점검",
		"maint.window":                  "점검 시간",
		"incident.past":                 "지난 인시던트",
		"incident.past_days":            "최근 %d일",
		"incident.none":                 "보고된 인시던트가 없습니다",
		"incident.affected":             "영향 받는 컴포넌트",
		"incident.started":              "시작",
		"incident.ended":                "해결",
		"incident.view":                 "상태 페이지에서 보기",
		"incident.back":                 "상태 페이지로",
		"incident.edited":               "수정됨",
		"incident.edited_at":            "%s에 수정",

		"admin.nav_checks":              "체크",
		"admin.nav_incidents":           "인시던트",
		"admin.incident_new":            "새 인시던트",
		"admin.incident_new_for":        "새 인시던트: %s",
		"admin.incident_title":          "제목",
		"admin.incident_stage":          "단계",
		"admin.incident_body":           "내용",
		"admin.incident_components":     "컴포넌트 상태",
		"admin.incident_components_tip": "선택한 상태가 해결될 때까지 체크 결과 대신 표시되고 가용률에도 들어갑니다",
		"admin.incident_unaffected":     "영향 없음",
		"admin.incident_notify":         "%s에 공지",
		"admin.incident_no_notifiers":   "이 페이지에는 공지 채널이 없습니다",
		"admin.incident_create":         "인시던트 만들기",
		"admin.incident_post":           "업데이트 올리기",
		"admin.incident_add_update":     "업데이트",
		"admin.incident_add_note":       "메모 추가",
		"admin.incident_resolved_note":  "해결된 인시던트입니다. 새 항목은 메모로 남고 컴포넌트 상태는 바뀌지 않습니다",
		"admin.incident_open":           "진행 중",
		"admin.incident_recent":         "최근 %d일 동안 해결",
		"admin.incident_none":           "없습니다",
		"admin.incident_delete":         "인시던트 삭제",
		"admin.incident_delete_confirm": "이 인시던트와 타임라인, 가용률에 준 영향까지 지웁니다",
		"admin.incident_timeline":       "타임라인",
		"admin.incident_public":         "공개 페이지에서 보기",
		"admin.incident_view_on":        "%s에서 보기",
		"admin.incident_notify_other":   "다른 페이지의 채널은 그 페이지 컴포넌트를 골랐을 때만 받습니다",
		"admin.incident_pages_tip":      "다른 페이지의 컴포넌트도 고를 수 있습니다. 각 페이지에는 그 페이지 컴포넌트만 보이고, 제목과 본문은 모든 페이지에 같이 나갑니다",
		"admin.open_page":               "페이지 보기",
		"admin.recent_outages":          "최근 24시간 장애",
		"admin.no_recent_outages":       "최근 24시간 동안 장애가 없었습니다",
		"admin.outage_start":            "장애 판정",
		"admin.cause":                   "원인",
		"admin.record_incident":         "인시던트로 기록",
		"admin.prefill_title":           "%s 장애",
		"admin.backfill":                "지난 장애 기록",
		"admin.maint_new":               "점검 예약",
		"admin.maint_new_for":           "%s 점검 예약",
		"admin.maint_times":             "점검 시간",
		"admin.maint_start":             "시작",
		"admin.maint_end":               "끝",
		"admin.maint_tip":               "시각은 페이지 시간대(%s) 기준입니다. 고른 컴포넌트는 이 시간 동안 점검 중으로 표시되고 가용률에서 빠집니다. 공지는 예약할 때 한 번 보내고, 시작과 끝에는 보내지 않습니다.",
		"admin.maint_create":            "점검 예약",
		"admin.maint_complete":          "지금 완료",
		"admin.maint_complete_tip":      "끝 시각을 지금으로 당깁니다",
		"admin.err_maint_times":         "점검 시작과 끝 시각을 넣어 주세요",
		"admin.err_maint_over":          "끝 시각이 이미 지났습니다",
		"admin.incident_edit":           "기록 수정",
		"admin.incident_save":           "저장",
		"admin.edit_tip":                "제목과 시각을 고칩니다. 시각은 페이지 시간대(%s) 기준이고, 시작 시각을 바꾸면 같은 때 시작한 컴포넌트 상태도 함께 옮겨집니다. 새로 공지하지는 않습니다.",
		"admin.edit_components_tip":     "바꾸면 시작부터 해결까지 같은 상태로 다시 저장되고, 그 기간의 가용률에도 반영됩니다.",
		"admin.err_edit_open":           "진행 중인 인시던트의 해결 시각과 컴포넌트는 업데이트로 바꿉니다",
		"admin.backfill_tip":            "이미 끝났거나 앞서 시작된 장애를 나중에 올릴 때 씁니다. 시각은 페이지 시간대(%s) 기준입니다. 해결 시각을 넣으면 해결됨 단계여야 하고, 고른 컴포넌트 상태는 시작부터 해결까지만 적용됩니다.",
		"admin.started_at":              "시작 시각",
		"admin.resolved_at":             "해결 시각",
		"admin.down_count":              "장애 %d",
		"admin.incident_back":           "인시던트 목록으로",
		"admin.update_edit":             "수정",
		"admin.update_edit_delete":      "수정, 삭제",
		"admin.update_edit_tip":         "고친 내용은 다시 공지하지 않습니다. 페이지에는 수정됨으로 표시됩니다",
		"admin.update_save":             "저장",
		"admin.update_latest_tip":       "가장 최근 업데이트는 지금 단계를 정하므로 지울 수 없습니다",
		"admin.update_delete":           "업데이트 삭제",
		"admin.update_delete_confirm":   "이 업데이트를 타임라인에서 지웁니다. 컴포넌트 상태는 그대로입니다",
		"admin.notify_ok":               "%s 전송 완료",
		"admin.notify_failed":           "%s 전송 실패(%d회 시도): %s",
		"admin.err_title":               "제목을 입력해 주세요",
		"admin.err_title_long":          "제목이 너무 깁니다",
		"admin.err_body":                "내용을 입력해 주세요",
		"admin.err_body_long":           "내용이 너무 깁니다",
		"admin.err_started_future":      "시작 시각이 미래입니다",
		"admin.err_resolved_future":     "해결 시각이 미래입니다",
		"admin.err_resolved_stage":      "해결 시각은 해결됨 단계에서만 지정할 수 있습니다",
		"admin.err_resolved_order":      "해결 시각은 시작 시각보다 뒤여야 합니다",
		"admin.err_time":                "시각 형식이 올바르지 않습니다",
		"admin.err_stage":               "단계를 골라 주세요",
		"admin.err_status":              "알 수 없는 상태가 들어왔습니다",
		"admin.err_delete_latest":       "가장 최근 업데이트는 지울 수 없습니다",
	},
	"en": {
		"status.operational":          "Operational",
		"status.degraded_performance": "Degraded performance",
		"status.partial_outage":       "Partial outage",
		"status.major_outage":         "Major outage",
		"status.under_maintenance":    "Under maintenance",
		"status.unknown":              "No data",
		"check.type.http":             "HTTP request",
		"check.type.tcp":              "TCP connection",
		"check.type.tls":              "TLS certificate",
		"check.type.domain_expiry":    "Domain expiry",
		"check.type.docker":           "Container",

		"banner.operational":          "All systems operational",
		"banner.degraded_performance": "Some systems are slow",
		"banner.partial_outage":       "Some systems are down",
		"banner.major_outage":         "Major outage",
		"banner.under_maintenance":    "Maintenance in progress",
		"banner.unknown":              "Checking status",

		"uptime.window":  "Past %d days",
		"uptime.percent": "%s%% uptime",
		"uptime.nodata":  "No data",
		"uptime.ago":     "%d days ago",
		"uptime.today":   "Today",
		"uptime.ongoing": "ongoing",
		"uptime.more":    "+%d more",
		"uptime.related": "Related",

		"page.updated":                 "Last updated",
		"card.title":                   "System status",
		"verdict.operational":          "We're not aware of any issues affecting our systems.",
		"verdict.degraded_performance": "Some systems are slow. The affected components are listed below.",
		"verdict.partial_outage":       "Some systems are down. The affected components are listed below.",
		"verdict.major_outage":         "We're seeing a major outage. The affected components are listed below.",
		"verdict.under_maintenance":    "Maintenance is in progress.",
		"verdict.unknown":              "Checks are starting. Status will appear shortly.",
		"group.count":                  "%d of %d operational",
		"page.list":                    "Status pages",
		"page.notfound":                "Page not found",
		"page.powered":                 "Powered by noroshi",
		"page.source":                  "Source on GitHub",
		"lang.switch":                  "한국어",
		"mode.to_light":                "Switch to light mode",
		"mode.to_dark":                 "Switch to dark mode",
		"lang.switch_to":               "ko",

		"admin.badge":            "Admin",
		"admin.checks":           "Checks",
		"admin.component":        "Component",
		"admin.check":            "Check",
		"admin.type":             "Type",
		"admin.target":           "Target",
		"admin.state":            "State",
		"admin.since":            "Since",
		"admin.last_run":         "Last run",
		"admin.latency":          "Latency",
		"admin.detail":           "Result",
		"admin.expiry":           "Expiry",
		"admin.results":          "Results (%s)",
		"admin.truncated":        "newest %[2]d of %[1]d",
		"admin.no_results_range": "No results in this period",
		"admin.range":            "Period",
		"admin.range_from":       "From",
		"admin.range_to":         "To",
		"admin.range_last":       "last %s",
		"admin.range_1h":         "1 hour",
		"admin.range_6h":         "6 hours",
		"admin.range_24h":        "24 hours",
		"admin.range_7d":         "7 days",
		"admin.range_14d":        "14 days",
		"admin.range_30d":        "30 days",
		"admin.range_90d":        "90 days",
		"admin.range_all":        "all %d days",
		"admin.failed_only":      "Failures only",
		"admin.latency_title":    "Latency (%s)",
		"admin.latency_chart":    "Latency over the period",
		"admin.lat_avg":          "avg",
		"admin.lat_max":          "max",
		"admin.lat_fails":        "%d of %d failed",
		"admin.lat_legend":       "The solid line is the average of passing results and the faint line the slowest. Red columns had failures, and the dashed lines are the amber and red limits. Hover a column for its numbers.",
		"admin.show":             "Show",
		"admin.err_range":        "The period could not be read, so the default period is shown",
		"admin.failures":         "Failures (%s)",
		"admin.fail_start":       "First failure",
		"admin.fail_end":         "Recovered",
		"admin.duration":         "Duration",
		"admin.fail_count":       "Failures",
		"admin.judged":           "Marked down",
		"admin.blip":             "Brief",
		"admin.failing":          "Still failing",
		"admin.no_failures":      "No failures in this period",
		"admin.streak":           "Streak",
		"admin.back":             "Back to list",
		"admin.logout":           "Sign out",
		"admin.signed_out_title": "You are signed out",
		"admin.signed_out_body":  "Your noroshi admin session has ended. Single sign-on for your other services is unchanged.",
		"admin.sign_in_again":    "Sign in again",
		"admin.signed_in":        "Signed in as",
		"admin.prober":           "Prober",
		"admin.prober_seen":      "Last active",
		"admin.prober_none":      "No prober run recorded yet",
		"admin.prober_stopped":   "No prober is running",
		"admin.this_leader":      "This instance is the leader",
		"admin.other_leader":     "Another instance is the leader",
		"admin.no_results":       "No results yet",
		"admin.days_left":        "%d days left",
		"admin.expired":          "Expired",
		"admin.dev_mode":         "Development mode: authentication is off",
		"admin.streak_ok":        "%d ok in a row",
		"admin.streak_fail":      "%d failed in a row",
		"admin.interval":         "Interval",
		"admin.timeout":          "Timeout",
		"admin.latency_limits":   "Latency limits",
		"admin.latency_tip":      "Amber above %s, red from %s",
		"admin.thresholds":       "Down / recover after",
		"admin.thresh_fmt":       "%d failures / %d successes in a row",
		"admin.page":             "Page",
		"admin.cert":             "Certificate",
		"admin.domain":           "Domain",
		"admin.skipped":          "Undecided",
		"nav.home":               "Home",

		"state.up":      "Up",
		"state.down":    "Down",
		"state.unknown": "Pending",
		"state.slow":    "Degraded",

		"error.forbidden":    "Only members of the admin group can sign in",
		"error.login_failed": "Sign-in failed. Please try again",
		"error.oidc_down":    "The sign-in server is unreachable",

		"page.private":                  "Private",
		"page.public":                   "Public",
		"page.doc":                      "Status",
		"page.refresh_in":               "Refresh in %s",
		"admin.nav_pages":               "Status pages",
		"admin.nav_label":               "Admin sections",
		"incident.stage.investigating":  "Investigating",
		"incident.stage.identified":     "Identified",
		"incident.stage.monitoring":     "Monitoring",
		"incident.stage.resolved":       "Resolved",
		"incident.stage.scheduled":      "Scheduled maintenance",
		"incident.stage.maint_upcoming": "Scheduled",
		"incident.stage.maint_active":   "In progress",
		"incident.stage.maint_done":     "Completed",
		"maint.upcoming":                "Scheduled maintenance",
		"maint.window":                  "Window",
		"incident.past":                 "Past incidents",
		"incident.past_days":            "Last %d days",
		"incident.none":                 "No incidents reported",
		"incident.affected":             "Affected components",
		"incident.started":              "Started",
		"incident.ended":                "Resolved",
		"incident.view":                 "View on the status page",
		"incident.back":                 "Back to the status page",
		"incident.edited":               "Edited",
		"incident.edited_at":            "Edited %s",

		"admin.nav_checks":              "Checks",
		"admin.nav_incidents":           "Incidents",
		"admin.incident_new":            "New incident",
		"admin.incident_new_for":        "New incident: %s",
		"admin.incident_title":          "Title",
		"admin.incident_stage":          "Stage",
		"admin.incident_body":           "Message",
		"admin.incident_components":     "Component status",
		"admin.incident_components_tip": "A chosen status replaces the check result until the incident is resolved, and counts toward uptime",
		"admin.incident_unaffected":     "Not affected",
		"admin.incident_notify":         "Announce to %s",
		"admin.incident_no_notifiers":   "No announcement channel is set for this page",
		"admin.incident_create":         "Create incident",
		"admin.incident_post":           "Post update",
		"admin.incident_add_update":     "Update",
		"admin.incident_add_note":       "Add note",
		"admin.incident_resolved_note":  "This incident is resolved. New entries are kept as notes and do not change component status",
		"admin.incident_open":           "Open",
		"admin.incident_recent":         "Resolved in the last %d days",
		"admin.incident_none":           "None",
		"admin.incident_delete":         "Delete incident",
		"admin.incident_delete_confirm": "Remove this incident, its timeline, and its effect on uptime",
		"admin.incident_timeline":       "Timeline",
		"admin.incident_public":         "View on the public page",
		"admin.incident_view_on":        "View on %s",
		"admin.incident_notify_other":   "Another page's channels get it only when one of that page's components is chosen",
		"admin.incident_pages_tip":      "Components on other pages can be chosen too. Each page shows only its own, and the title and text appear on every page",
		"admin.open_page":               "View page",
		"admin.recent_outages":          "Outages in the last 24 hours",
		"admin.no_recent_outages":       "No outages in the last 24 hours",
		"admin.outage_start":            "Marked down",
		"admin.cause":                   "Cause",
		"admin.record_incident":         "Record as incident",
		"admin.prefill_title":           "%s outage",
		"admin.backfill":                "Record a past outage",
		"admin.maint_new":               "Schedule maintenance",
		"admin.maint_new_for":           "Schedule maintenance for %s",
		"admin.maint_times":             "Maintenance window",
		"admin.maint_start":             "Starts",
		"admin.maint_end":               "Ends",
		"admin.maint_tip":               "Times are in the page's time zone (%s). The chosen components show as under maintenance during the window and are left out of uptime. The announcement goes out once, when you schedule it, not at the start or end.",
		"admin.maint_create":            "Schedule",
		"admin.maint_complete":          "Complete now",
		"admin.maint_complete_tip":      "Moves the end to now",
		"admin.err_maint_times":         "Give the start and end of the maintenance",
		"admin.err_maint_over":          "The end time has already passed",
		"admin.incident_edit":           "Correct the record",
		"admin.incident_save":           "Save",
		"admin.edit_tip":                "Corrects the title and times. Times are in the page's time zone (%s). Moving the start moves the component statuses that began with it. Nothing is announced.",
		"admin.edit_components_tip":     "A change stores these statuses again for the whole period from start to resolve, and the uptime of that period follows.",
		"admin.err_edit_open":           "An open incident's resolve time and components change with an update",
		"admin.backfill_tip":            "For an outage that already ended or began earlier. Times are in the page's time zone (%s). A resolve time needs the resolved stage, and the chosen component statuses then apply from the start to the resolve time only.",
		"admin.started_at":              "Started at",
		"admin.resolved_at":             "Resolved at",
		"admin.down_count":              "%d down",
		"admin.incident_back":           "Back to incidents",
		"admin.update_edit":             "Edit",
		"admin.update_edit_delete":      "Edit or delete",
		"admin.update_edit_tip":         "Edits are not announced again. The page marks the update as edited",
		"admin.update_save":             "Save",
		"admin.update_latest_tip":       "The latest update sets the current stage, so it cannot be deleted",
		"admin.update_delete":           "Delete update",
		"admin.update_delete_confirm":   "Remove this update from the timeline. Component statuses stay as they are",
		"admin.notify_ok":               "Sent to %s",
		"admin.notify_failed":           "%s failed after %d tries: %s",
		"admin.err_title":               "Enter a title",
		"admin.err_title_long":          "The title is too long",
		"admin.err_body":                "Enter a message",
		"admin.err_body_long":           "The message is too long",
		"admin.err_started_future":      "The start time is in the future",
		"admin.err_resolved_future":     "The resolve time is in the future",
		"admin.err_resolved_stage":      "A resolve time needs the resolved stage",
		"admin.err_resolved_order":      "The resolve time must come after the start time",
		"admin.err_time":                "The time is not valid",
		"admin.err_stage":               "Choose a stage",
		"admin.err_status":              "Unknown status",
		"admin.err_delete_latest":       "The latest update cannot be deleted",
	},
}
