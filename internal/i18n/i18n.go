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

		"banner.operational":          "모든 시스템 정상",
		"banner.degraded_performance": "일부 시스템 성능 저하",
		"banner.partial_outage":       "일부 시스템 장애",
		"banner.major_outage":         "주요 장애 발생",
		"banner.under_maintenance":    "점검 진행 중",
		"banner.unknown":              "상태 확인 중",

		"uptime.window":  "최근 %d일",
		"uptime.percent": "가용률 %s%%",
		"uptime.nodata":  "데이터 없음",
		"uptime.down":    "장애 %s",
		"uptime.ago":     "%d일 전",
		"uptime.today":   "오늘",

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

		"admin.title":          "noroshi 관리",
		"admin.checks":         "체크",
		"admin.component":      "컴포넌트",
		"admin.check":          "체크",
		"admin.type":           "종류",
		"admin.target":         "대상",
		"admin.state":          "상태",
		"admin.since":          "변경 시각",
		"admin.last_run":       "마지막 실행",
		"admin.latency":        "응답 시간",
		"admin.detail":         "결과",
		"admin.expiry":         "만료",
		"admin.recent":         "최근 결과",
		"admin.streak":         "연속",
		"admin.back":           "목록으로",
		"admin.logout":         "로그아웃",
		"admin.signed_in":      "로그인",
		"admin.prober":         "prober",
		"admin.prober_seen":    "마지막 활동",
		"admin.prober_none":    "아직 실행 기록이 없습니다",
		"admin.prober_stopped": "실행 중인 prober가 없습니다",
		"admin.this_leader":    "이 인스턴스가 리더",
		"admin.other_leader":   "다른 인스턴스가 리더",
		"admin.no_results":     "아직 결과가 없습니다",
		"admin.days_left":      "%d일 남음",
		"admin.expired":        "만료됨",
		"admin.dev_mode":       "개발 모드: 인증이 꺼져 있습니다",
		"admin.streak_fmt":     "연속 성공 %d, 실패 %d",
		"admin.interval":       "주기",
		"admin.timeout":        "타임아웃",
		"admin.thresholds":     "장애/회복 기준",
		"admin.thresh_fmt":     "연속 실패 %d회 / 연속 성공 %d회",
		"admin.page":           "페이지",
		"admin.cert":           "인증서",
		"admin.domain":         "도메인",
		"admin.skipped":        "판단 보류",
		"nav.home":             "처음으로",

		"state.up":      "정상",
		"state.down":    "장애",
		"state.unknown": "확인 중",
		"state.slow":    "느림",

		"error.forbidden":    "관리자 그룹에 속한 계정만 들어올 수 있습니다",
		"error.login_failed": "로그인에 실패했습니다. 다시 시도해 주세요",
		"error.oidc_down":    "로그인 서버에 연결할 수 없습니다",

		"page.private":                 "비공개",
		"page.refresh_in":              "%s 후 새로고침",
		"admin.nav_pages":              "상태 페이지",
		"incident.stage.investigating": "조사 중",
		"incident.stage.identified":    "원인 확인",
		"incident.stage.monitoring":    "지켜보는 중",
		"incident.stage.resolved":      "해결됨",
		"incident.past":                "지난 인시던트",
		"incident.past_days":           "최근 %d일",
		"incident.none":                "보고된 인시던트가 없습니다",
		"incident.affected":            "영향 받는 컴포넌트",
		"incident.started":             "시작",
		"incident.ended":               "해결",
		"incident.view":                "상태 페이지에서 보기",
		"incident.back":                "상태 페이지로",

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
		"admin.incident_back":           "인시던트 목록으로",
		"admin.notify_ok":               "%s 전송 완료",
		"admin.notify_failed":           "%s 전송 실패(%d회 시도): %s",
		"admin.err_title":               "제목을 입력해 주세요",
		"admin.err_title_long":          "제목이 너무 깁니다",
		"admin.err_body":                "내용을 입력해 주세요",
		"admin.err_body_long":           "내용이 너무 깁니다",
		"admin.err_stage":               "단계를 골라 주세요",
		"admin.err_status":              "알 수 없는 상태가 들어왔습니다",
	},
	"en": {
		"status.operational":          "Operational",
		"status.degraded_performance": "Degraded performance",
		"status.partial_outage":       "Partial outage",
		"status.major_outage":         "Major outage",
		"status.under_maintenance":    "Under maintenance",
		"status.unknown":              "No data",

		"banner.operational":          "All systems operational",
		"banner.degraded_performance": "Some systems are slow",
		"banner.partial_outage":       "Some systems are down",
		"banner.major_outage":         "Major outage",
		"banner.under_maintenance":    "Maintenance in progress",
		"banner.unknown":              "Checking status",

		"uptime.window":  "Past %d days",
		"uptime.percent": "%s%% uptime",
		"uptime.nodata":  "No data",
		"uptime.down":    "%s down",
		"uptime.ago":     "%d days ago",
		"uptime.today":   "Today",

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

		"admin.title":          "noroshi admin",
		"admin.checks":         "Checks",
		"admin.component":      "Component",
		"admin.check":          "Check",
		"admin.type":           "Type",
		"admin.target":         "Target",
		"admin.state":          "State",
		"admin.since":          "Since",
		"admin.last_run":       "Last run",
		"admin.latency":        "Latency",
		"admin.detail":         "Result",
		"admin.expiry":         "Expiry",
		"admin.recent":         "Recent results",
		"admin.streak":         "Streak",
		"admin.back":           "Back to list",
		"admin.logout":         "Sign out",
		"admin.signed_in":      "Signed in as",
		"admin.prober":         "Prober",
		"admin.prober_seen":    "Last active",
		"admin.prober_none":    "No prober run recorded yet",
		"admin.prober_stopped": "No prober is running",
		"admin.this_leader":    "This instance is the leader",
		"admin.other_leader":   "Another instance is the leader",
		"admin.no_results":     "No results yet",
		"admin.days_left":      "%d days left",
		"admin.expired":        "Expired",
		"admin.dev_mode":       "Development mode: authentication is off",
		"admin.streak_fmt":     "%d ok, %d failed in a row",
		"admin.interval":       "Interval",
		"admin.timeout":        "Timeout",
		"admin.thresholds":     "Down / recover after",
		"admin.thresh_fmt":     "%d failures / %d successes in a row",
		"admin.page":           "Page",
		"admin.cert":           "Certificate",
		"admin.domain":         "Domain",
		"admin.skipped":        "Undecided",
		"nav.home":             "Home",

		"state.up":      "Up",
		"state.down":    "Down",
		"state.unknown": "Pending",
		"state.slow":    "Slow",

		"error.forbidden":    "Only members of the admin group can sign in",
		"error.login_failed": "Sign-in failed. Please try again",
		"error.oidc_down":    "The sign-in server is unreachable",

		"page.private":                 "Private",
		"page.refresh_in":              "Refresh in %s",
		"admin.nav_pages":              "Status pages",
		"incident.stage.investigating": "Investigating",
		"incident.stage.identified":    "Identified",
		"incident.stage.monitoring":    "Monitoring",
		"incident.stage.resolved":      "Resolved",
		"incident.past":                "Past incidents",
		"incident.past_days":           "Last %d days",
		"incident.none":                "No incidents reported",
		"incident.affected":            "Affected components",
		"incident.started":             "Started",
		"incident.ended":               "Resolved",
		"incident.view":                "View on the status page",
		"incident.back":                "Back to the status page",

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
		"admin.incident_back":           "Back to incidents",
		"admin.notify_ok":               "Sent to %s",
		"admin.notify_failed":           "%s failed after %d tries: %s",
		"admin.err_title":               "Enter a title",
		"admin.err_title_long":          "The title is too long",
		"admin.err_body":                "Enter a message",
		"admin.err_body_long":           "The message is too long",
		"admin.err_stage":               "Choose a stage",
		"admin.err_status":              "Unknown status",
	},
}
