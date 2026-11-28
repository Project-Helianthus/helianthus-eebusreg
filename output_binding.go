package eebusruntime

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

const (
	cemAddress                = "cem"
	addressFileName           = "entity-addresses"
	useCaseConsumption        = "Limitation of Power Consumption"
	featureMeasurement        = "measurement"
	featureDiagnosis          = "device diagnosis"
	featureElectrical         = "electrical connection"
	featureLoad               = "load control"
	operationProductionLimit  = "pv.operation.set_active_power_limit"
	operationAllocatedCurrent = "evse.operation.set_allocated_current"
	operationActivePower      = "active power"
)

var (
	errAddressStore             = errors.New("entity address store is unavailable")
	errStoredAddressReplacement = errors.New("stored address replacement is rejected")
	errMergedResources          = errors.New("merged resources")
	errExistingNode             = errors.New("existing node is unavailable")
)

// outputBindingGate serialises every binding read and write, so a host may
// apply lens results and read entities from different goroutines.
var outputBindingGate sync.Mutex

// shipNode is the node of one runtime service.
type shipNode interface {
	AdvertisementCount() int
	ServiceCount() int
	DeviceType() string
	EntityTypes() []string
	AddEntity(kind, address, parent string) error
	ServerFeatures(address string) []string
	AddServerFeature(address, name string) error
	AddServerFeatures(address string, names []string) (string, error)
}

// outputEntityRemover removes one entity from the node, with the features and
// use-case support that entity carried.
type outputEntityRemover interface {
	RemoveEntity(address string) error
}

// outputNodeSource is a runtime in this package that owns one output node.
type outputNodeSource interface {
	outputNode() (*eebusfacade.OutputNode, error)
}

// outputNodeCarrier is a runtime from another package that presents the node it owns.
type outputNodeCarrier interface {
	OutputNode() (*eebusfacade.OutputNode, error)
}

func outputNodeOfRuntime(runtime Runtime) (*eebusfacade.OutputNode, error) {
	if source, ok := runtime.(outputNodeSource); ok {
		return openedOutputNode(source.outputNode())
	}
	if source, ok := runtime.(outputNodeCarrier); ok {
		return openedOutputNode(source.OutputNode())
	}
	return nil, errExistingNode
}

func openedOutputNode(node *eebusfacade.OutputNode, err error) (*eebusfacade.OutputNode, error) {
	if err != nil || node == nil {
		return nil, errExistingNode
	}
	return node, nil
}

// openShipNode attaches to the node of this runtime. It never composes a second service.
var openShipNode = func(runtime Runtime) (shipNode, error) {
	node, err := outputNodeOfRuntime(runtime)
	if err != nil || node == nil {
		return nil, errExistingNode
	}
	return node, nil
}

var syncBindingFile = func(file *os.File) error {
	return file.Sync()
}

var knownUseCases = map[string]struct{}{
	"Monitoring of Inverter":               {},
	"Monitoring of PV String":              {},
	"Limitation of Power Production":       {},
	"EVSE Commissioning and Configuration": {},
	"Monitoring of Power Consumption":      {},
	useCaseConsumption:                     {},
}

// OutputBinding maps an admitted lens result onto this node's server features.
// It keeps one advertisement, one service, device type EnergyManagementSystem, and the CEM entity.
type OutputBinding struct {
	directory  string
	addresses  map[string]string
	byResource map[string]boundEntity
	order      []string
	node       shipNode
	gaps       map[string][]string
	runtime    Runtime
	operations interface{}
	fallbacks  []OutputPowerLimitFallback
}

type boundEntity struct {
	kind      string
	address   string
	parent    string
	withdrawn bool
}

// LensResult is an admitted lens result. It carries no SPINE type.
// Facts are the promoted SemReg elements of that resource. MemberSum is the
// same-cut member sum of an EVSE, when the cut asked for one.
type LensResult struct {
	Family           string
	ResourceID       string
	ParentResourceID string
	LensAdmitted     bool
	RuntimeEnabled   bool
	InputDimension   bool
	ContainsCited    bool
	Basis            string
	Elements         []string
	Operations       []LensOperation
	UseCase          string
	Actor            string
	UseCaseText      string
	DeviceActor      string
	MissingElements  []string
	Overlapping      bool
	Facts            []LensFact
	MemberSum        *LensMemberSum
}

// LensOperation is one operation on a lens result. It carries no SPINE type.
type LensOperation struct {
	Name          string
	Unit          string
	OwnCapability bool
	Converted     bool
}

// LensFact is one promoted SemReg element carried on a lens result.
// Phase and Connector are the dimension values of that element.
// The quantity stays in its SemReg unit.
type LensFact struct {
	Element   string
	Phase     string
	Connector string
	Value     LensValue
	Unit      string
	Quality   LensQuality
}

// LensValue is the typed value of one SemReg element.
type LensValue struct {
	Kind        string
	Coefficient string
	Exponent    int32
	Boolean     bool
	Text        string
	Symbol      string
}

// LensQuality keeps the SemReg quality axes as independent strings.
type LensQuality struct {
	Assertion     string
	Qualification string
	Promotion     string
	Validity      string
	Availability  string
	Freshness     string
}

// LensMemberSum is the same-cut member sum of one EVSE, or the reason it was withheld.
type LensMemberSum struct {
	Withheld         bool
	Coefficient      string
	Exponent         int32
	Unit             string
	Reason           string
	NotIncludedCount int
	NotIncluded      []LensNotIncluded
}

// LensNotIncluded is one asset a cut did not include, with the reason.
type LensNotIncluded struct {
	Asset  string
	Reason string
}

// NewOutputBinding binds output onto this runtime's node and loads addresses stored in directory.
func NewOutputBinding(runtime Runtime, directory string) (*OutputBinding, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errAddressStore
	}
	addresses, err := loadAddresses(filepath.Join(directory, addressFileName))
	if err != nil {
		return nil, err
	}
	node, err := openShipNode(runtime)
	if err != nil || node == nil {
		return nil, errExistingNode
	}
	return &OutputBinding{
		directory:  directory,
		addresses:  addresses,
		byResource: map[string]boundEntity{},
		node:       node,
		gaps:       map[string][]string{},
		runtime:    runtime,
	}, nil
}

// liveNodeLocked resolves this runtime's node on every call. When that runtime
// replaced its service, the binding forgets the entities it added on the old
// node, keeps the stored addresses, and adds each entity again when a lens
// result names it. Without that service it has no node.
func (b *OutputBinding) liveNodeLocked() shipNode {
	if b == nil {
		return nil
	}
	node, err := openShipNode(b.runtime)
	if err != nil || node == nil {
		node = nil
	}
	if node != b.node {
		b.byResource = map[string]boundEntity{}
		b.order = nil
		b.gaps = map[string][]string{}
		b.node = node
	}
	return node
}

// Apply adds the entity of an admitted lens result. A PV String is added only
// when its parent is an Inverter already applied from this binding. A use-case
// text that forbids device type EnergyManagementSystem adds nothing and names
// that prohibition. Apply also serves the use cases of an inverter, a PV
// string or a charger, and MonitoringLoss returns what that serving did not
// publish.
func (b *OutputBinding) Apply(result LensResult) []string {
	if b == nil {
		return applyLoss(errExistingNode)
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	delete(b.gaps, result.ResourceID)
	if !b.admitted(result) {
		return announcementLoss(result)
	}
	if node == nil {
		return applyLoss(errExistingNode)
	}
	if loss := b.prohibitedDeviceType(result); loss != nil {
		return loss
	}
	if err := b.ensureEntity(result); err != nil {
		return applyLoss(err)
	}
	if !result.Overlapping {
		served := withExtensionElements(result)
		switch result.Family {
		case "charger":
			// A rejected charger feature withdraws only the use case or the
			// operation that needs it, so the entity stays applied and
			// MonitoringLoss names that feature.
			b.recordGaps(result.ResourceID, b.serveChargerOutput(served).Loss)
			return announcementLoss(result)
		case "inverter", "pv string":
			if err := b.bindFeatures(result); err != nil {
				return applyLoss(err)
			}
			outcome := b.serveInverterMonitoring(served)
			outcome.Loss = append(outcome.Loss, extensionLoss(result)...)
			sort.Strings(outcome.Loss)
			b.recordGaps(result.ResourceID, outcome.Loss)
			if monitoringFeatureGap(outcome) {
				return applyLoss(errExistingNode)
			}
		default:
			return announcementLoss(result)
		}
	}
	return announcementLoss(result)
}

// ApplyCut marks overlapping operations across the admitted results and applies
// each result once, in the order given. The legacy family filter is not used.
func (b *OutputBinding) ApplyCut(results []LensResult) [][]string {
	copied := append([]LensResult(nil), results...)
	selectionMarkConflicts(copied)
	losses := make([][]string, len(copied))
	for index := range copied {
		losses[index] = b.Apply(copied[index])
	}
	return losses
}

// RetainSession keeps the scoped operation port and the detached power-limit
// fallbacks of this instance. A later handler of this binding reads this port.
func (b *OutputBinding) RetainSession(operations interface{}, fallbacks []OutputPowerLimitFallback) {
	if b == nil {
		return
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	b.operations = operations
	b.fallbacks = append([]OutputPowerLimitFallback(nil), fallbacks...)
}

// Withdraw removes the resource's entity from the node. The stored address
// remains, so a later cut adds that same entity again.
func (b *OutputBinding) Withdraw(resourceID string) error {
	if b == nil {
		return errExistingNode
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	entity, ok := b.byResource[resourceID]
	if !ok || entity.withdrawn || entity.address == "" {
		return nil
	}
	if node == nil {
		return errExistingNode
	}
	remover, ok := node.(outputEntityRemover)
	if !ok {
		return errExistingNode
	}
	if err := remover.RemoveEntity(entity.address); err != nil {
		return err
	}
	entity.withdrawn = true
	b.byResource[resourceID] = entity
	delete(b.gaps, resourceID)
	return nil
}

// EntityPresent reports whether the resource's entity is on the node.
func (b *OutputBinding) EntityPresent(resourceID string) bool {
	if b == nil {
		return false
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	if b.liveNodeLocked() == nil {
		return false
	}
	entity, ok := b.byResource[resourceID]
	return ok && !entity.withdrawn && entity.address != ""
}

// RetainedOperations returns the scoped operation port retained for this binding.
func (b *OutputBinding) RetainedOperations() interface{} {
	if b == nil {
		return nil
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	return b.operations
}

// PowerLimitFallbacks returns a copy of the power-limit fallbacks retained for this binding.
func (b *OutputBinding) PowerLimitFallbacks() []OutputPowerLimitFallback {
	if b == nil {
		return nil
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	return append([]OutputPowerLimitFallback(nil), b.fallbacks...)
}

// withExtensionElements returns a copy of the result whose elements also list
// each scenario element an extension element of its family fills.
func withExtensionElements(result LensResult) LensResult {
	result.Elements = extensionElements(result.Family, result.Elements, result.MissingElements)
	return result
}

// extensionLoss names each scenario element of the entity that the result
// does not fill with its extension element.
func extensionLoss(result LensResult) []string {
	return extensionLosses(result.Family, result.Elements, result.MissingElements)
}

// MonitoringLoss returns what the last Apply of a resource identifier did not
// publish for the use cases of its entity: each mandatory element or scenario
// the lens result does not fill, string DC energy for a string and
// line-to-line voltage for an inverter when the result does not carry them,
// each server feature the node rejected, and, for a charger, the device
// classification gap and the loss of an active-power consumption-limit write.
// It is empty when that Apply published everything, was not admitted, or
// stopped before serving the entity.
func (b *OutputBinding) MonitoringLoss(resourceID string) []string {
	if b == nil {
		return nil
	}
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	if b.liveNodeLocked() == nil {
		return nil
	}
	return append([]string(nil), b.gaps[resourceID]...)
}

func (b *OutputBinding) recordGaps(resourceID string, loss []string) {
	if len(loss) == 0 {
		return
	}
	if b.gaps == nil {
		b.gaps = map[string][]string{}
	}
	b.gaps[resourceID] = append([]string(nil), loss...)
}

// AdvertisementCount is the advertisement count of the existing node.
func (b *OutputBinding) AdvertisementCount() int {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	if node == nil {
		return 0
	}
	return node.AdvertisementCount()
}

// ServiceCount is the service count of the existing node.
func (b *OutputBinding) ServiceCount() int {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	if node == nil {
		return 0
	}
	return node.ServiceCount()
}

// DeviceType is the device type of the existing node.
func (b *OutputBinding) DeviceType() string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	if node == nil {
		return ""
	}
	return node.DeviceType()
}

// EntityTypes lists the CEM entity and each admitted entity on the existing node.
func (b *OutputBinding) EntityTypes() []string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	if node == nil {
		return nil
	}
	return node.EntityTypes()
}

// EntityAddress returns the address stored for a resource identifier.
func (b *OutputBinding) EntityAddress(resourceID string) string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	if b.liveNodeLocked() == nil {
		return ""
	}
	return b.byResource[resourceID].address
}

// ParentAddress returns the parent entity address of a resource identifier.
func (b *OutputBinding) ParentAddress(resourceID string) string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	if b.liveNodeLocked() == nil {
		return ""
	}
	return b.byResource[resourceID].parent
}

// EntityAddresses lists the CEM address and every stored entity address.
func (b *OutputBinding) EntityAddresses() []string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	addresses := []string{cemAddress}
	if b.liveNodeLocked() == nil {
		return addresses
	}
	for _, id := range b.resourceOrder() {
		addresses = append(addresses, b.byResource[id].address)
	}
	return addresses
}

// ServerFeatures lists the server features mapped for a resource identifier.
func (b *OutputBinding) ServerFeatures(resourceID string) []string {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	node := b.liveNodeLocked()
	if node == nil {
		return nil
	}
	address := b.byResource[resourceID].address
	if address == "" {
		return nil
	}
	return node.ServerFeatures(address)
}

func (b *OutputBinding) admitted(result LensResult) bool {
	if !publishedFamily(result) || result.ResourceID == "" || result.Basis != "" {
		return false
	}
	if strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	return result.Family != "pv string" || b.stringParentAdmitted(result)
}

func publishedFamily(result LensResult) bool {
	if !result.LensAdmitted || !result.RuntimeEnabled {
		return false
	}
	switch result.Family {
	case "inverter", "charger", "ev", "meter", "battery", "heating circuit", "heating zone", "room", "grid":
		return true
	case "pv string":
		return !result.InputDimension
	default:
		return false
	}
}

func (b *OutputBinding) stringParentAdmitted(result LensResult) bool {
	if !result.ContainsCited || result.ParentResourceID == "" {
		return false
	}
	parent, ok := b.byResource[result.ParentResourceID]
	return ok && parent.kind == "Inverter"
}

func (b *OutputBinding) prohibitedDeviceType(result LensResult) []string {
	if b == nil || b.node == nil || result.Actor == "" || result.UseCaseText == "" {
		return nil
	}
	deviceType := b.node.DeviceType()
	if deviceType == "" || !textForbidsDeviceType(result.UseCaseText, result.Actor, deviceType) {
		return nil
	}
	return []string{"device_type_prohibition", deviceType}
}

func textForbidsDeviceType(text, actor, deviceType string) bool {
	for text != "" {
		sentence := text
		if cut := strings.IndexByte(text, '.'); cut >= 0 {
			sentence = text[:cut]
			text = text[cut+1:]
		} else {
			text = ""
		}
		if sentenceForbidsDeviceType(sentence, actor, deviceType) {
			return true
		}
	}
	return false
}

func sentenceForbidsDeviceType(sentence, actor, deviceType string) bool {
	forbidAt := indexWord(sentence, "forbids")
	actorAt := indexWord(sentence, actor)
	if forbidAt < 0 || actorAt < 0 || actorAt > forbidAt {
		return false
	}
	clause := sentence[forbidAt:]
	if exceptAt := indexWord(clause, "except"); exceptAt >= 0 {
		clause = clause[:exceptAt]
	}
	return indexWord(clause, deviceType) >= 0
}

func indexWord(text, word string) int {
	if word == "" || len(word) > len(text) {
		return -1
	}
	for start := 0; start <= len(text)-len(word); {
		at := strings.Index(text[start:], word)
		if at < 0 {
			return -1
		}
		at += start
		end := at + len(word)
		before := at == 0 || !wordByte(text[at-1])
		after := end == len(text) || !wordByte(text[end])
		if before && after {
			return at
		}
		start = at + 1
	}
	return -1
}

func wordByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func (b *OutputBinding) ensureEntity(result LensResult) error {
	kind := entityKind(result.Family)
	existing, exists := b.byResource[result.ResourceID]
	if exists && !existing.withdrawn {
		if existing.kind != kind {
			return errMergedResources
		}
		return nil
	}
	if exists && existing.kind != kind {
		return errMergedResources
	}
	address, err := b.allocate(result.ResourceID)
	if err != nil {
		return err
	}
	parent := ""
	if result.Family != "meter" && result.ParentResourceID != "" {
		if cited, ok := b.byResource[result.ParentResourceID]; ok {
			parent = cited.address
		}
	}
	if b.node == nil {
		return errExistingNode
	}
	if err := b.node.AddEntity(kind, address, parent); err != nil {
		return errExistingNode
	}
	b.byResource[result.ResourceID] = boundEntity{kind: kind, address: address, parent: parent}
	if !exists {
		b.order = append(b.order, result.ResourceID)
	}
	return nil
}

func entityKind(family string) string {
	switch family {
	case "inverter":
		return "Inverter"
	case "pv string":
		return "PV String"
	case "charger":
		return "EVSE"
	case "ev":
		return "EV"
	case "meter":
		return "SubMeterElectricity"
	case "battery":
		return "Battery"
	case "heating circuit":
		return "HeatingCircuit"
	case "heating zone":
		return "HeatingZone"
	case "room":
		return "HVACRoom"
	case "grid":
		return "GridConnectionPointOfPremises"
	default:
		return ""
	}
}

func (b *OutputBinding) allocate(resourceID string) (string, error) {
	if address, ok := b.addresses[resourceID]; ok {
		return address, nil
	}
	address := b.freshAddress()
	if err := b.noteAddressLocked(resourceID, address); err != nil {
		return "", err
	}
	return address, nil
}

func (b *OutputBinding) freshAddress() string {
	used := map[string]struct{}{cemAddress: {}}
	for _, address := range b.addresses {
		used[address] = struct{}{}
	}
	ordinal := 0
	for {
		token := letterToken(ordinal)
		if _, taken := used[token]; !taken {
			return token
		}
		ordinal++
	}
}

func letterToken(ordinal int) string {
	value := ordinal
	var token []byte
	for {
		token = append(token, byte('a'+value%26))
		value /= 26
		if value == 0 {
			break
		}
		value--
	}
	for left, right := 0, len(token)-1; left < right; left, right = left+1, right-1 {
		token[left], token[right] = token[right], token[left]
	}
	return string(token)
}

func (b *OutputBinding) noteAddress(resourceID, address string) error {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	return b.noteAddressLocked(resourceID, address)
}

func (b *OutputBinding) noteAddressLocked(resourceID, address string) error {
	if resourceID == "" || !letterAddress(address) {
		return errAddressStore
	}
	if current, ok := b.addresses[resourceID]; ok {
		if current != address {
			return errStoredAddressReplacement
		}
		return nil
	}
	b.addresses[resourceID] = address
	if err := b.persist(); err != nil {
		delete(b.addresses, resourceID)
		return err
	}
	return nil
}

func letterAddress(value string) bool {
	if value == "" || value == cemAddress {
		return false
	}
	for _, r := range value {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func (b *OutputBinding) persist() error {
	directory, err := os.Open(b.directory)
	if err != nil {
		return errAddressStore
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		_ = directory.Close()
		return errAddressStore
	}
	payload := renderAddresses(b.addresses)
	path := filepath.Join(b.directory, addressFileName)
	temporary := path + ".new"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		_ = directory.Close()
		return errAddressStore
	}
	_, writeErr := file.Write(payload)
	syncErr := syncBindingFile(file)
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		_ = directory.Close()
		return errAddressStore
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		_ = directory.Close()
		return errAddressStore
	}
	dirSyncErr := directory.Sync()
	dirCloseErr := directory.Close()
	if dirSyncErr != nil || dirCloseErr != nil {
		return errAddressStore
	}
	return nil
}

func renderAddresses(addresses map[string]string) []byte {
	ids := make([]string, 0, len(addresses))
	for id := range addresses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var builder strings.Builder
	for _, id := range ids {
		builder.WriteString(id)
		builder.WriteByte(' ')
		builder.WriteString(addresses[id])
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

func loadAddresses(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, errAddressStore
	}
	addresses := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		resourceID, address, ok := strings.Cut(line, " ")
		if !ok || resourceID == "" || strings.ContainsAny(resourceID, " \t") || !letterAddress(address) {
			return nil, errAddressStore
		}
		if current, exists := addresses[resourceID]; exists && current != address {
			return nil, errStoredAddressReplacement
		}
		addresses[resourceID] = address
	}
	return addresses, nil
}

func (b *OutputBinding) bindFeatures(result LensResult) error {
	if b.node == nil {
		return errExistingNode
	}
	address := b.byResource[result.ResourceID].address
	blocked := map[string]struct{}{}
	for _, name := range result.MissingElements {
		blocked[name] = struct{}{}
	}
	present := map[string]struct{}{}
	for _, name := range b.node.ServerFeatures(address) {
		present[name] = struct{}{}
	}
	for _, element := range result.Elements {
		if _, skip := blocked[element]; skip {
			continue
		}
		for _, feature := range featuresForElement(result.Family, element) {
			present[feature] = struct{}{}
		}
	}
	for _, operation := range result.Operations {
		for _, feature := range featuresForOperation(result.Family, operation) {
			present[feature] = struct{}{}
		}
	}
	names := make([]string, 0, len(present))
	for name := range present {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := b.node.AddServerFeature(address, name); err != nil {
			return errExistingNode
		}
	}
	return nil
}

func featuresForElement(family, element string) []string {
	if family != "inverter" {
		return nil
	}
	switch element {
	case "inverter ac measurement", "energy":
		return []string{featureElectrical, featureMeasurement}
	case "temperature":
		return []string{featureMeasurement}
	case "operating state":
		return []string{featureDiagnosis}
	default:
		return nil
	}
}

func featuresForOperation(family string, operation LensOperation) []string {
	if !operation.OwnCapability || operation.Converted {
		return nil
	}
	switch family {
	case "inverter":
		if operation.Name == operationProductionLimit && operation.Unit == "W" {
			return []string{featureElectrical, featureLoad}
		}
	case "charger":
		if operation.Name == operationAllocatedCurrent && operation.Unit == "A" {
			return []string{featureElectrical, featureLoad}
		}
	}
	return nil
}

func announcementLoss(result LensResult) []string {
	if result.Overlapping {
		return []string{"conflict"}
	}
	if result.DeviceActor == "EV" {
		return []string{"EV"}
	}
	if len(result.MissingElements) > 0 {
		return append([]string(nil), result.MissingElements...)
	}
	if result.UseCase == useCaseConsumption && !consumptionApplied(result) {
		return []string{"consumption_limit_not_applied"}
	}
	if result.UseCase != "" {
		if _, known := knownUseCases[result.UseCase]; !known {
			return []string{"not_announced"}
		}
	}
	return nil
}

func consumptionApplied(result LensResult) bool {
	if result.Family == "charger" || result.Overlapping {
		return false
	}
	for _, operation := range result.Operations {
		if operation.OwnCapability && !operation.Converted && operation.Unit == "W" && operation.Name == operationActivePower {
			return true
		}
	}
	return false
}

func applyLoss(err error) []string {
	switch {
	case errors.Is(err, errMergedResources):
		return []string{"merged_resources"}
	case errors.Is(err, errStoredAddressReplacement):
		return []string{"stored_address_replacement"}
	case errors.Is(err, errExistingNode):
		return []string{"existing_node"}
	default:
		return []string{"address_store"}
	}
}

func (b *OutputBinding) resourceOrder() []string {
	ids := append([]string(nil), b.order...)
	sort.Slice(ids, func(left, right int) bool {
		return b.byResource[ids[left]].address < b.byResource[ids[right]].address
	})
	return ids
}

// outputNode returns the node owned by this runtime instance.
func (runtime *runtimeImplementation) outputNode() (*eebusfacade.OutputNode, error) {
	if runtime == nil {
		return nil, errExistingNode
	}
	runtime.mu.Lock()
	backend := runtime.backend
	runtime.mu.Unlock()
	source, ok := backend.(outputNodeSource)
	if !ok {
		return nil, errExistingNode
	}
	return source.outputNode()
}

// outputNode returns the node stored with this runtime's service backend.
func (backend *facadeRuntimeBackend) outputNode() (*eebusfacade.OutputNode, error) {
	if backend == nil || backend.backend == nil {
		return nil, errExistingNode
	}
	node, err := eebusfacade.OpenOutputNode(backend.backend)
	if err != nil || node == nil {
		return nil, errExistingNode
	}
	return node, nil
}
