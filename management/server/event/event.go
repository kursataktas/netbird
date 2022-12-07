package event

import "time"

const (
	// DeviceEvent describes an event that happened of a device (e.g, connected/disconnected)
	DeviceEvent Type = "device"
	// ManagementEvent describes an event that happened on a Management service (e.g., user added)
	ManagementEvent Type = "management"
)

const (
	AddPeerByUserOperation Operation = iota
	AddPeerWithKeyOperation
	UserJoinedOperation
)

const (
	AddPeerByUserOperationMessage  string = "Add new peer"
	AddPeerWithKeyOperationMessage string = AddPeerByUserOperationMessage
	UserJoinedOperationMessage     string = "New user joined"
)

// MessageForOperation returns a string message for an Operation
func MessageForOperation(op Operation) string {
	switch op {
	case AddPeerByUserOperation:
		return AddPeerByUserOperationMessage
	case AddPeerWithKeyOperation:
		return AddPeerWithKeyOperationMessage
	case UserJoinedOperation:
		return UserJoinedOperationMessage
	default:
		return "UNKNOWN_OPERATION"
	}
}

// Type of the Event
type Type string

// Operation is an action that triggered an Event
type Operation int

// Store provides an interface to store or stream events.
type Store interface {
	// Save an event in the store
	Save(event Event) (*Event, error)
	// Get returns "limit" number of events from the "offset" index ordered descending or ascending by a timestamp
	Get(accountID string, offset, limit int, descending bool) ([]Event, error)
	// Close the sink flushing events if necessary
	Close() error
}

// Event represents a network/system activity event.
type Event struct {
	// Timestamp of the event
	Timestamp time.Time
	// Operation that was performed during the event
	Operation string
	// OperationCode that was performed during the event
	OperationCode Operation
	// ID of the event (can be empty, meaning that it wasn't yet generated)
	ID uint64
	// Type of the event
	Type Type
	// ModifierID is the ID of an object that modifies a Target
	ModifierID string
	// TargetID is the ID of an object that a Modifier modifies
	TargetID string
	// AccountID where event happened
	AccountID string
}

// Copy the event
func (e *Event) Copy() *Event {
	return &Event{
		Timestamp:  e.Timestamp,
		Operation:  e.Operation,
		ID:         e.ID,
		Type:       e.Type,
		ModifierID: e.ModifierID,
		TargetID:   e.TargetID,
		AccountID:  e.AccountID,
	}
}
