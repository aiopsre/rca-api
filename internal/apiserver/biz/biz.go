package biz

import (
	"errors"
	"sync"

	aijobv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	alerteventv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alert_event"
	alertingpolicyv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alerting_policy"
	evidencev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/evidence"
	incidentv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/incident"
	internalstrategyconfigv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
	noticev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/notice"
	orchestratorskillsetv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_skillset"
	orchestratorstrategyv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_strategy"
	orchestratortemplatev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_template"
	orchestratortoolsetv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_toolset"
	playbookv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/playbook"
	rbacv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/rbac"
	sessionv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	silencev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/silence"
	triggerv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	"github.com/google/wire"

	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

// ProviderSet declares dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz defines the access points for various business logic modules.
//
//nolint:interfacebloat // Aggregation interface intentionally exposes all domain biz modules.
type IBiz interface {
	IncidentV1() incidentv1.IncidentBiz
	AlertEventV1() alerteventv1.AlertEventBiz
	EvidenceV1() evidencev1.EvidenceBiz
	AIJobV1() aijobv1.AIJobBiz
	InternalStrategyConfigV1() internalstrategyconfigv1.ConfigBiz
	OrchestratorStrategyV1() orchestratorstrategyv1.StrategyBiz
	OrchestratorSkillsetV1() orchestratorskillsetv1.SkillsetBiz
	OrchestratorTemplateV1() orchestratortemplatev1.TemplateBiz
	OrchestratorToolsetV1() orchestratortoolsetv1.ToolsetBiz
	RBACV1() rbacv1.RBACBiz
	SessionV1() sessionv1.SessionBiz
	SilenceV1() silencev1.SilenceBiz
	TriggerV1() triggerv1.TriggerBiz
	NoticeV1() noticev1.NoticeBiz
	AlertingPolicyV1() alertingpolicyv1.AlertingPolicyBiz
	PlaybookV1() playbookv1.PlaybookBiz
	Close() error
}

// biz is the concrete implementation of the business logic IBiz.
type biz struct {
	store store.IStore

	incidentOnce               sync.Once
	incidentBiz                incidentv1.IncidentBiz
	alertEventOnce             sync.Once
	alertEventBiz              alerteventv1.AlertEventBiz
	evidenceOnce               sync.Once
	evidenceBiz                evidencev1.EvidenceBiz
	aiJobOnce                  sync.Once
	aiJobBiz                   aijobv1.AIJobBiz
	internalStrategyConfigOnce sync.Once
	internalStrategyConfigBiz  internalstrategyconfigv1.ConfigBiz
	orchestratorStrategyOnce   sync.Once
	orchestratorStrategyBiz    orchestratorstrategyv1.StrategyBiz
	orchestratorSkillsetOnce   sync.Once
	orchestratorSkillsetBiz    orchestratorskillsetv1.SkillsetBiz
	orchestratorTemplateOnce   sync.Once
	orchestratorTemplateBiz    orchestratortemplatev1.TemplateBiz
	orchestratorToolsetOnce    sync.Once
	orchestratorToolsetBiz     orchestratortoolsetv1.ToolsetBiz
	rbacOnce                   sync.Once
	rbacBiz                    rbacv1.RBACBiz
	sessionOnce                sync.Once
	sessionBiz                 sessionv1.SessionBiz
	silenceOnce                sync.Once
	silenceBiz                 silencev1.SilenceBiz
	triggerOnce                sync.Once
	triggerBiz                 triggerv1.TriggerBiz
	noticeOnce                 sync.Once
	noticeBiz                  noticev1.NoticeBiz
	alertingPolicyOnce         sync.Once
	alertingPolicyBiz          alertingpolicyv1.AlertingPolicyBiz
	playbookOnce               sync.Once
	playbookBiz                playbookv1.PlaybookBiz

	closeOnce sync.Once
	closeErr  error
}

// Ensure biz implements IBiz at compile time.
var _ IBiz = (*biz)(nil)

// NewBiz creates and returns a new instance of the business logic layer.
func NewBiz(store store.IStore) *biz {
	return &biz{store: store}
}

func (b *biz) IncidentV1() incidentv1.IncidentBiz {
	b.incidentOnce.Do(func() {
		b.incidentBiz = incidentv1.New(b.store)
	})
	return b.incidentBiz
}

func (b *biz) AlertEventV1() alerteventv1.AlertEventBiz {
	b.alertEventOnce.Do(func() {
		b.alertEventBiz = alerteventv1.New(b.store)
	})
	return b.alertEventBiz
}

func (b *biz) EvidenceV1() evidencev1.EvidenceBiz {
	b.evidenceOnce.Do(func() {
		b.evidenceBiz = evidencev1.New(b.store)
	})
	return b.evidenceBiz
}

func (b *biz) AIJobV1() aijobv1.AIJobBiz {
	b.aiJobOnce.Do(func() {
		b.aiJobBiz = aijobv1.New(b.store)
	})
	return b.aiJobBiz
}

func (b *biz) InternalStrategyConfigV1() internalstrategyconfigv1.ConfigBiz {
	b.internalStrategyConfigOnce.Do(func() {
		b.internalStrategyConfigBiz = internalstrategyconfigv1.New(b.store)
	})
	return b.internalStrategyConfigBiz
}

func (b *biz) OrchestratorStrategyV1() orchestratorstrategyv1.StrategyBiz {
	b.orchestratorStrategyOnce.Do(func() {
		b.orchestratorStrategyBiz = orchestratorstrategyv1.New(b.store)
	})
	return b.orchestratorStrategyBiz
}

func (b *biz) OrchestratorSkillsetV1() orchestratorskillsetv1.SkillsetBiz {
	b.orchestratorSkillsetOnce.Do(func() {
		b.orchestratorSkillsetBiz = orchestratorskillsetv1.New(b.store)
	})
	return b.orchestratorSkillsetBiz
}

func (b *biz) OrchestratorTemplateV1() orchestratortemplatev1.TemplateBiz {
	b.orchestratorTemplateOnce.Do(func() {
		b.orchestratorTemplateBiz = orchestratortemplatev1.New()
	})
	return b.orchestratorTemplateBiz
}

func (b *biz) OrchestratorToolsetV1() orchestratortoolsetv1.ToolsetBiz {
	b.orchestratorToolsetOnce.Do(func() {
		b.orchestratorToolsetBiz = orchestratortoolsetv1.New(b.store)
	})
	return b.orchestratorToolsetBiz
}

func (b *biz) RBACV1() rbacv1.RBACBiz {
	b.rbacOnce.Do(func() {
		b.rbacBiz = rbacv1.New(b.store)
	})
	return b.rbacBiz
}

func (b *biz) SessionV1() sessionv1.SessionBiz {
	b.sessionOnce.Do(func() {
		b.sessionBiz = sessionv1.New(b.store)
	})
	return b.sessionBiz
}

func (b *biz) SilenceV1() silencev1.SilenceBiz {
	b.silenceOnce.Do(func() {
		b.silenceBiz = silencev1.New(b.store)
	})
	return b.silenceBiz
}

func (b *biz) TriggerV1() triggerv1.TriggerBiz {
	b.triggerOnce.Do(func() {
		b.triggerBiz = triggerv1.New(b.store)
	})
	return b.triggerBiz
}

func (b *biz) NoticeV1() noticev1.NoticeBiz {
	b.noticeOnce.Do(func() {
		b.noticeBiz = noticev1.New(b.store)
	})
	return b.noticeBiz
}

func (b *biz) AlertingPolicyV1() alertingpolicyv1.AlertingPolicyBiz {
	b.alertingPolicyOnce.Do(func() {
		b.alertingPolicyBiz = alertingpolicyv1.New(b.store)
	})
	return b.alertingPolicyBiz
}

func (b *biz) PlaybookV1() playbookv1.PlaybookBiz {
	b.playbookOnce.Do(func() {
		b.playbookBiz = playbookv1.New(b.store)
	})
	return b.playbookBiz
}

func (b *biz) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var errs []error
		errs = appendCloseIfSupported(errs, b.incidentBiz)
		errs = appendCloseIfSupported(errs, b.alertEventBiz)
		errs = appendCloseIfSupported(errs, b.evidenceBiz)
		errs = appendCloseIfSupported(errs, b.aiJobBiz)
		errs = appendCloseIfSupported(errs, b.internalStrategyConfigBiz)
		errs = appendCloseIfSupported(errs, b.orchestratorStrategyBiz)
		errs = appendCloseIfSupported(errs, b.orchestratorSkillsetBiz)
		errs = appendCloseIfSupported(errs, b.orchestratorTemplateBiz)
		errs = appendCloseIfSupported(errs, b.orchestratorToolsetBiz)
		errs = appendCloseIfSupported(errs, b.rbacBiz)
		errs = appendCloseIfSupported(errs, b.sessionBiz)
		errs = appendCloseIfSupported(errs, b.silenceBiz)
		errs = appendCloseIfSupported(errs, b.triggerBiz)
		errs = appendCloseIfSupported(errs, b.noticeBiz)
		errs = appendCloseIfSupported(errs, b.alertingPolicyBiz)
		errs = appendCloseIfSupported(errs, b.playbookBiz)
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}

func appendCloseIfSupported(errs []error, target any) []error {
	if target == nil {
		return errs
	}
	closer, ok := target.(interface{ Close() error })
	if !ok {
		return errs
	}
	return append(errs, closer.Close())
}
