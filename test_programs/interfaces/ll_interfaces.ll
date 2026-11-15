source_filename = "<fixture>/src/main.peep"

@.table.0 = private unnamed_addr constant [2 x ptr] [ptr @"ll_interfaces:main.Square::area", ptr @"ll_interfaces:main.Square::grow"]
@.str.0 = private unnamed_addr constant [2 x i8] c"\0A\00"

define i32 @"ll_interfaces:main.Square::area"(ptr %a0) {
entry:
  %s0 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store ptr %a0, ptr %s0
  %i1.a = load ptr, ptr %s0
  %i1 = load i32, ptr %i1.a
  %i2.a = load ptr, ptr %s0
  %i2 = load i32, ptr %i2.a
  %i3 = mul i32 %i1, %i2
  ret i32 %i3
}

define void @"ll_interfaces:main.Square::grow"(ptr %a0, i32 %a1) {
entry:
  %s0 = alloca [8 x i8], align 8
  %s1 = alloca [4 x i8], align 4
  br label %bb0
bb0:
  store ptr %a0, ptr %s0
  store i32 %a1, ptr %s1
  %i2.a = load ptr, ptr %s0
  %i2 = load i32, ptr %i2.a
  %i3 = load i32, ptr %s1
  %i4 = add i32 %i2, %i3
  %i5 = load ptr, ptr %s0
  store i32 %i4, ptr %i5
  ret void
}

define i32 @"ll_interfaces:main.grown"(ptr %a0) {
entry:
  %s1 = alloca [16 x i8], align 8
  %s2 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  %i0 = load ptr, ptr %a0
  %i1.a = getelementptr inbounds i8, ptr %a0, i64 8
  %i1 = load ptr, ptr %i1.a
  store ptr %i0, ptr %s1
  %i3 = getelementptr inbounds i8, ptr %s1, i64 8
  store ptr %i1, ptr %i3
  %i4 = load ptr, ptr %s1
  %i5.a = getelementptr inbounds i8, ptr %s1, i64 8
  %i5 = load ptr, ptr %i5.a
  %i6.a = getelementptr inbounds i8, ptr %i5, i64 8
  %i6 = load ptr, ptr %i6.a
  call void %i6(ptr %i4, i32 1)
  %i9 = load ptr, ptr %a0
  %i10.a = getelementptr inbounds i8, ptr %a0, i64 8
  %i10 = load ptr, ptr %i10.a
  store ptr %i9, ptr %s2
  %i12 = getelementptr inbounds i8, ptr %s2, i64 8
  store ptr %i10, ptr %i12
  %i13 = load ptr, ptr %s2
  %i14.a = getelementptr inbounds i8, ptr %s2, i64 8
  %i14 = load ptr, ptr %i14.a
  %i15 = load ptr, ptr %i14
  %i16 = call i32 %i15(ptr %i13)
  ret i32 %i16
}

define void @"ll_interfaces:main.main"() {
entry:
  %s0 = alloca [4 x i8], align 4
  %s1 = alloca [8 x i8], align 8
  %s2 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  store i32 2, ptr %s0
  store ptr %s0, ptr %s2
  %i3 = getelementptr inbounds i8, ptr %s2, i64 8
  store ptr @.table.0, ptr %i3
  %i5 = call i32 @"ll_interfaces:main.grown"(ptr %s2)
  %i6 = sext i32 %i5 to i64
  store i64 %i6, ptr %s1
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s1)
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  call void @"peeper_rt_v1_flush"()
  ret void
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

