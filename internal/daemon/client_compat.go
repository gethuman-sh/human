package daemon

import (
	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/settings"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

// This file is the migration shim for the (addr, token) call convention that
// Client replaces. It exists only so consumers can move package by package;
// it is deleted once the last one has.

// legacyClient rebuilds a Client from the two strings a caller still holds. It
// skips the protocol gate on purpose — a wrapper cannot report a second error
// its signature has no room for, and the gate lands on these call sites as they
// move to Connect.
func legacyClient(addr, token string) *Client {
	return &Client{info: DaemonInfo{Addr: addr, Token: token}, version: ClientVersion}
}

// RunRemote connects to the daemon at addr, sends the CLI args, and returns the
// exit code.
func RunRemote(addr, token string, args []string, version string) (int, error) {
	c := legacyClient(addr, token)
	c.version = version
	return c.RunRemote(args)
}

func GetConfirmStatus(addr, token string, id string) (ConfirmStatus, error) {
	return legacyClient(addr, token).GetConfirmStatus(id)
}

func RunRemoteCapture(addr, token string, args []string) ([]byte, error) {
	return legacyClient(addr, token).RunRemoteCapture(args)
}

func QueryAudit(addr, token string, filterArgs []string) ([]audit.Event, error) {
	return legacyClient(addr, token).QueryAudit(filterArgs)
}

func GetLogMode(addr, token string) (string, error) {
	return legacyClient(addr, token).GetLogMode()
}

func SetLogMode(addr, token string, mode string) (string, error) {
	return legacyClient(addr, token).SetLogMode(mode)
}

func GetHookSnapshot(addr, token string) (map[string]hookevents.SessionSnapshot, error) {
	return legacyClient(addr, token).GetHookSnapshot()
}

func GetNetworkEvents(addr, token string) ([]NetworkEvent, error) {
	return legacyClient(addr, token).GetNetworkEvents()
}

func GetModelOutcomes(addr, token string) ([]proxy.ModelCallOutcome, error) {
	return legacyClient(addr, token).GetModelOutcomes()
}

func GetTrackerDiagnose(addr, token string) ([]tracker.TrackerStatus, error) {
	return legacyClient(addr, token).GetTrackerDiagnose()
}

func GetConfig(addr, token string) (settings.Doc, error) {
	return legacyClient(addr, token).GetConfig()
}

func SetConfig(addr, token string, req SetConfigRequest) (settings.Doc, error) {
	return legacyClient(addr, token).SetConfig(req)
}

func GetTrackerIssues(addr, token string) ([]TrackerIssuesResult, error) {
	return legacyClient(addr, token).GetTrackerIssues()
}

func GetTrackerIssuesLite(addr, token string) ([]TrackerIssuesResult, error) {
	return legacyClient(addr, token).GetTrackerIssuesLite()
}

func GetTrackerIssue(addr, token string, trackerKind, trackerName, key string) (*IssueDetailResult, error) {
	return legacyClient(addr, token).GetTrackerIssue(trackerKind, trackerName, key)
}

func GetBoardView(addr, token string) (BoardView, error) {
	return legacyClient(addr, token).GetBoardView()
}

func GetCurrentUserName(addr, token string) (string, error) {
	return legacyClient(addr, token).GetCurrentUserName()
}

func BoardTransition(addr, token string, req BoardTransitionRequest) error {
	return legacyClient(addr, token).BoardTransition(req)
}

func BoardFix(addr, token string, req BoardFixRequest) error {
	return legacyClient(addr, token).BoardFix(req)
}

func BoardSecurityFix(addr, token string, req SecurityFixRequest) error {
	return legacyClient(addr, token).BoardSecurityFix(req)
}

func SendBoardOption(addr, token string, req BoardOptionRequest) error {
	return legacyClient(addr, token).SendBoardOption(req)
}

func GenerateFeatures(addr, token string) error {
	return legacyClient(addr, token).GenerateFeatures()
}

func StartFindbugs(addr, token string) error {
	return legacyClient(addr, token).StartFindbugs()
}

func StartFindsecurity(addr, token string) error {
	return legacyClient(addr, token).StartFindsecurity()
}

func CloseTicket(addr, token string, req CloseTicketRequest) error {
	return legacyClient(addr, token).CloseTicket(req)
}

func CreateMocks(addr, token string, req CreateMocksRequest) error {
	return legacyClient(addr, token).CreateMocks(req)
}

func CreateVariations(addr, token string, req CreateVariationsRequest) error {
	return legacyClient(addr, token).CreateVariations(req)
}

func ChooseMockup(addr, token string, req ChooseMockupRequest) error {
	return legacyClient(addr, token).ChooseMockup(req)
}

func PruneMockup(addr, token string, req PruneMockupRequest) error {
	return legacyClient(addr, token).PruneMockup(req)
}

func IdeationStart(addr, token string, req IdeationStartRequest) (IdeationStatus, error) {
	return legacyClient(addr, token).IdeationStart(req)
}

func IdeationReply(addr, token string, req IdeationReplyRequest) (IdeationStatus, error) {
	return legacyClient(addr, token).IdeationReply(req)
}

func IdeationApprove(addr, token string, req IdeationApproveRequest) (IdeationStatus, error) {
	return legacyClient(addr, token).IdeationApprove(req)
}

func IdeaCreate(addr, token string, req IdeaCreateRequest) (IdeaCreateResponse, error) {
	return legacyClient(addr, token).IdeaCreate(req)
}

func BugCreate(addr, token string, req BugCreateRequest) (BugCreateResponse, error) {
	return legacyClient(addr, token).BugCreate(req)
}

func Relate(addr, token string, req RelateRequest) error {
	return legacyClient(addr, token).Relate(req)
}

func SecurityCreate(addr, token string, req SecurityCreateRequest) (SecurityCreateResponse, error) {
	return legacyClient(addr, token).SecurityCreate(req)
}

func GetIdeationStatus(addr, token string) (IdeationStatus, error) {
	return legacyClient(addr, token).GetIdeationStatus()
}

func GetPendingConfirms(addr, token string) ([]PendingConfirm, error) {
	return legacyClient(addr, token).GetPendingConfirms()
}

func GetDaemonBusy(addr, token string) (DaemonBusyStatus, error) {
	return legacyClient(addr, token).GetDaemonBusy()
}

func GetDoctor(addr, token string, refresh bool) (DoctorData, error) {
	return legacyClient(addr, token).GetDoctor(refresh)
}

func GetToolStats(addr, token string) (*stats.ToolStats, error) {
	return legacyClient(addr, token).GetToolStats()
}

func GetStatsOverview(addr, token string, rng string) (*StatsOverview, error) {
	return legacyClient(addr, token).GetStatsOverview(rng)
}

func GetTicketCost(addr, token string, key string) (costledger.TicketCost, error) {
	return legacyClient(addr, token).GetTicketCost(key)
}

func SendConfirmDecision(addr, token string, id string, approved bool) error {
	return legacyClient(addr, token).SendConfirmDecision(id, approved)
}

func Subscribe(addr, token string) (<-chan SubscribeEvent, func(), error) {
	return legacyClient(addr, token).Subscribe()
}

func FSMWhere(addr, token string, req WhereRequest) (WhereReport, error) {
	return legacyClient(addr, token).FSMWhere(req)
}
