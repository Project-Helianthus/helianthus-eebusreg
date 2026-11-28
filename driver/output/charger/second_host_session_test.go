package charger_test

import (
	"context"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

type hostSession struct {
	eebusservicebridge.PairingSessionSurface
	root semdriver.NodeRef
}

func (session *hostSession) ScopedOperations() semdriver.ScopedOperations { return session }
func (session *hostSession) Calendar() semdriver.Calendar                 { return session }
func (session *hostSession) Publisher() semdriver.Publisher               { return session }
func (session *hostSession) Prepare(func(*semreg.PublicationBatch)) (semreg.PublicationBatch, error) {
	return semreg.PublicationBatch{}, nil
}
func (session *hostSession) RegisterExecutor(semdriver.SettingRoute, semdriver.Executor) (func(), error) {
	return nil, nil
}
func (session *hostSession) Invoke(context.Context, semdriver.OperationRequest) (semdriver.OperationResult, error) {
	return semdriver.OperationResult{}, nil
}
func (session *hostSession) InvokeCallBound(context.Context, semdriver.OperationRequest) (semdriver.OperationResult, error) {
	return semdriver.OperationResult{}, nil
}
func (session *hostSession) BeginAcquisition() semdriver.CalendarAcquisition { return nil }
func (session *hostSession) Root() semdriver.NodeRef                         { return session.root }
func (session *hostSession) Node(semdriver.NodeSpec) (semdriver.NodeRef, error) {
	return session.root, nil
}
func (session *hostSession) ReadMember(semdriver.NodeRef, semdriver.MemberSpec, semdriver.NativeReader) error {
	return nil
}
func (session *hostSession) Method(semdriver.NodeRef, semdriver.MethodSpec, semdriver.NativeMethod) error {
	return nil
}
func (session *hostSession) View(semdriver.ViewSpec) error { return nil }
func (session *hostSession) Begin(context.Context) (semdriver.PublicationTransaction, error) {
	return session, nil
}
func (session *hostSession) OperatorLinks([]semreg.NativeBinding) []semreg.IdentityLink { return nil }
func (session *hostSession) AdmitManufacturerSerial(semdriver.ManufacturerSerialClaim) error {
	return nil
}
func (session *hostSession) Stage(semreg.PublicationBatch) error              { return nil }
func (session *hostSession) DeclareSource(semdriver.Source) error             { return nil }
func (session *hostSession) DeclareSettingRoute(semdriver.SettingRoute) error { return nil }
func (session *hostSession) StageSample(semdriver.Sample) error               { return nil }
func (session *hostSession) StageLink(semdriver.LinkClaim) error              { return nil }
func (session *hostSession) Commit(context.Context) error                     { return nil }
func (session *hostSession) Abort(context.Context) error                      { return nil }
