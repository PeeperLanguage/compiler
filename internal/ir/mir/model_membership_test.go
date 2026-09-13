package mir

// The two node sets, asserted at compile time. Instr and Terminator are
// structurally identical apart from their markers, so these declarations are
// what keep a node in the set its position requires: drop a marker, or move a
// node between the sets, and this file stops compiling.
//
// This is membership only. MIR lowering and the backend both reject unknown
// members at their dispatch boundaries, so omissions fail loudly rather than
// being accepted by a source-shape meta-test.
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
