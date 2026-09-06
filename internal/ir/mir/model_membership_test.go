package mir

// The two node sets, asserted at compile time. Instr and Terminator are
// structurally identical apart from their markers, so these declarations are
// what keep a node in the set its position requires: drop a marker, or move a
// node between the sets, and this file stops compiling.
//
// This is membership only. That every member is actually classified — by MIR
// lowering and by the backend — is a separate contract, held by
// contracts.TestEveryLoweredNodeKindHasAPhaseDecision.
var (
	_ Instr = (*Assign)(nil)
	_ Instr = (*Store)(nil)
	_ Instr = (*Print)(nil)
	_ Instr = (*Drop)(nil)
	_ Instr = (*DynamicArrayOp)(nil)
	_ Instr = (*Call)(nil)
	_ Instr = (*InterfaceCall)(nil)

	_ Terminator = (*Jump)(nil)
	_ Terminator = (*Branch)(nil)
	_ Terminator = (*SwitchVariant)(nil)
	_ Terminator = (*Ret)(nil)
)
