source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.table.0 = private unnamed_addr constant [2 x ptr] [ptr @"ll_interfaces:main.Square::area", ptr @"ll_interfaces:main.Square::grow"]
@.str.0 = private unnamed_addr constant [2 x i8] c"\0A\00"

define i32 @"ll_interfaces:main.Square::area"(ptr %arg0) {
entry:
  %v0 = alloca ptr
  br label %bb0
bb0:
  store ptr %arg0, ptr %v0
  %i1.f.0 = load ptr, ptr %v0
  %i1.f = getelementptr inbounds i8, ptr %i1.f.0, i64 0
  %i1 = load i32, ptr %i1.f
  %i2.f.0 = load ptr, ptr %v0
  %i2.f = getelementptr inbounds i8, ptr %i2.f.0, i64 0
  %i2 = load i32, ptr %i2.f
  %i3 = mul i32 %i1, %i2
  ret i32 %i3
}

define void @"ll_interfaces:main.Square::grow"(ptr %arg0, i32 %arg1) {
entry:
  %v0 = alloca ptr
  %v1 = alloca i32
  br label %bb0
bb0:
  store ptr %arg0, ptr %v0
  store i32 %arg1, ptr %v1
  %i2.f.0 = load ptr, ptr %v0
  %i2.f = getelementptr inbounds i8, ptr %i2.f.0, i64 0
  %i2 = load i32, ptr %i2.f
  %i3 = load i32, ptr %v1
  %i4 = add i32 %i2, %i3
  %i5.f.0 = load ptr, ptr %v0
  %i5.f = getelementptr inbounds i8, ptr %i5.f.0, i64 0
  store i32 %i4, ptr %i5.f
  ret void
}

define i32 @"ll_interfaces:main.grown"({ ptr, ptr } %arg0) {
entry:
  %v0 = alloca { ptr, ptr }
  br label %bb0
bb0:
  store { ptr, ptr } %arg0, ptr %v0
  %i1 = load { ptr, ptr }, ptr %v0
  %i2.d = extractvalue { ptr, ptr } %i1, 0
  %i2.t = extractvalue { ptr, ptr } %i1, 1
  %i2.s = getelementptr inbounds ptr, ptr %i2.t, i32 1
  %i2.f = load ptr, ptr %i2.s
  call void %i2.f(ptr %i2.d, i32 1)
  %i3 = load { ptr, ptr }, ptr %v0
  %i4.d = extractvalue { ptr, ptr } %i3, 0
  %i4.t = extractvalue { ptr, ptr } %i3, 1
  %i4.s = getelementptr inbounds ptr, ptr %i4.t, i32 0
  %i4.f = load ptr, ptr %i4.s
  %i4 = call i32 %i4.f(ptr %i4.d)
  ret i32 %i4
}

define void @"ll_interfaces:main.main"() {
entry:
  %v0 = alloca [4 x i8], align 4
  %i5.0.m = alloca i64
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds i8, ptr %v0, i64 0
  store i32 2, ptr %i0.0
  %i2 = getelementptr inbounds i8, ptr %v0, i64 0
  %i3.a = insertvalue { ptr, ptr } poison, ptr %i2, 0
  %i3 = insertvalue { ptr, ptr } %i3.a, ptr @.table.0, 1
  %i4 = call i32 @"ll_interfaces:main.grown"({ ptr, ptr } %i3)
  %i5.0.x = sext i32 %i4 to i64
  store i64 %i5.0.x, ptr %i5.0.m
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %i5.0.m)
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1)
  call void @"peeper_rt_v1_flush"()
  ret void
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

