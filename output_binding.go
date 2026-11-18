package eebusruntime

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	deviceTypeName            = "EnergyManagementSystem"
	cemAddress                = "cem"
	cemKind                   = "CEM"
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
)

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
	features   map[string][]string
}

type boundEntity struct {
	kind    string
	address string
	parent  string
}

type lensResult struct {
	Family           string
	ResourceID       string
	ParentResourceID string
	LensAdmitted     bool
	RuntimeEnabled   bool
	InputDimension   bool
	ContainsCited    bool
	Basis            string
	Elements         []string
	Operations       []lensOperation
	UseCase          string
	Actor            string
	UseCaseText      string
	DeviceActor      string
	MissingElements  []string
	Overlapping      bool
}

type lensOperation struct {
	Name          string
	Unit          string
	OwnCapability bool
	Converted     bool
}

// NewOutputBinding binds output onto the existing node and loads addresses stored in directory.
func NewOutputBinding(directory string) (*OutputBinding, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errAddressStore
	}
	addresses, err := loadAddresses(filepath.Join(directory, addressFileName))
	if err != nil {
		return nil, err
	}
	return &OutputBinding{
		directory:  directory,
		addresses:  addresses,
		byResource: map[string]boundEntity{},
		features:   map[string][]string{},
	}, nil
}

// Apply adds an Inverter, a PV String whose parent is that Inverter, or an EVSE
// entity when the publication contract admits the lens result. A use-case text
// that forbids device type EnergyManagementSystem adds nothing and names that prohibition.
func (b *OutputBinding) Apply(result lensResult) []string {
	if !b.admitted(result) {
		return announcementLoss(result)
	}
	if loss := prohibitedDeviceType(result); loss != nil {
		return loss
	}
	if err := b.ensureEntity(result); err != nil {
		return applyLoss(err)
	}
	if !result.Overlapping {
		b.bindFeatures(result)
	}
	return announcementLoss(result)
}

// AdvertisementCount is the one advertisement of the existing node.
func (*OutputBinding) AdvertisementCount() int { return 1 }

// ServiceCount is the one service of the existing node.
func (*OutputBinding) ServiceCount() int { return 1 }

// DeviceType is the device type of the existing node.
func (*OutputBinding) DeviceType() string { return deviceTypeName }

// EntityTypes lists the CEM entity and each admitted entity.
func (b *OutputBinding) EntityTypes() []string {
	types := []string{cemKind}
	for _, id := range b.resourceOrder() {
		types = append(types, b.byResource[id].kind)
	}
	return types
}

// EntityAddress returns the address stored for a resource identifier.
func (b *OutputBinding) EntityAddress(resourceID string) string {
	return b.byResource[resourceID].address
}

// ParentAddress returns the parent entity address of a resource identifier.
func (b *OutputBinding) ParentAddress(resourceID string) string {
	return b.byResource[resourceID].parent
}

// EntityAddresses lists the CEM address and every stored entity address.
func (b *OutputBinding) EntityAddresses() []string {
	addresses := []string{cemAddress}
	for _, id := range b.resourceOrder() {
		addresses = append(addresses, b.byResource[id].address)
	}
	return addresses
}

// ServerFeatures lists the server features mapped for a resource identifier.
func (b *OutputBinding) ServerFeatures(resourceID string) []string {
	return append([]string(nil), b.features[resourceID]...)
}

func (b *OutputBinding) admitted(result lensResult) bool {
	if !publishedFamily(result) || result.ResourceID == "" || result.Basis != "" {
		return false
	}
	if strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	return result.Family != "pv string" || b.stringParentAdmitted(result)
}

func publishedFamily(result lensResult) bool {
	if !result.LensAdmitted || !result.RuntimeEnabled {
		return false
	}
	switch result.Family {
	case "inverter", "charger":
		return true
	case "pv string":
		return !result.InputDimension
	default:
		return false
	}
}

func (b *OutputBinding) stringParentAdmitted(result lensResult) bool {
	if !result.ContainsCited || result.ParentResourceID == "" {
		return false
	}
	parent, ok := b.byResource[result.ParentResourceID]
	return ok && parent.kind == "Inverter"
}

func prohibitedDeviceType(result lensResult) []string {
	if result.Actor == "" || result.UseCaseText == "" {
		return nil
	}
	if !textForbidsDeviceType(result.UseCaseText, result.Actor, deviceTypeName) {
		return nil
	}
	return []string{"device_type_prohibition", deviceTypeName}
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

func (b *OutputBinding) ensureEntity(result lensResult) error {
	kind := entityKind(result.Family)
	if existing, ok := b.byResource[result.ResourceID]; ok {
		if existing.kind != kind {
			return errMergedResources
		}
		return nil
	}
	address, err := b.allocate(result.ResourceID)
	if err != nil {
		return err
	}
	entity := boundEntity{kind: kind, address: address}
	if result.Family == "pv string" {
		entity.parent = b.byResource[result.ParentResourceID].address
	}
	b.byResource[result.ResourceID] = entity
	b.order = append(b.order, result.ResourceID)
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
	default:
		return ""
	}
}

func (b *OutputBinding) allocate(resourceID string) (string, error) {
	if address, ok := b.addresses[resourceID]; ok {
		return address, nil
	}
	address := b.freshAddress()
	if err := b.noteAddress(resourceID, address); err != nil {
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

func (b *OutputBinding) bindFeatures(result lensResult) {
	blocked := map[string]struct{}{}
	for _, name := range result.MissingElements {
		blocked[name] = struct{}{}
	}
	present := map[string]struct{}{}
	for _, name := range b.features[result.ResourceID] {
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
	b.features[result.ResourceID] = names
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

func featuresForOperation(family string, operation lensOperation) []string {
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

func announcementLoss(result lensResult) []string {
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

func consumptionApplied(result lensResult) bool {
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
