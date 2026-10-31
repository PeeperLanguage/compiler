source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [1 x i8] c"\00"
@.str.1 = private unnamed_addr constant [3 x i8] c", \00"
@.str.2 = private unnamed_addr constant [2 x i8] c"[\00"
@.str.3 = private unnamed_addr constant [2 x i8] c"]\00"
@.str.4 = private unnamed_addr constant [3 x i8] c"]\0A\00"

define void @"ll_print_arrays:main.main"() !dbg !3 {
entry:
  %v0 = alloca [2 x [3 x i32]]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !10, metadata !DIExpression()), !dbg !11
  %i0 = alloca [3 x i32]
  %i1 = alloca [3 x i32]
  %i4 = alloca [2 x [3 x i32]]
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds [3 x i32], ptr %i0, i32 0, i64 0, !dbg !12
  store i32 1, ptr %i0.0, !dbg !12
  %i0.1 = getelementptr inbounds [3 x i32], ptr %i0, i32 0, i64 1, !dbg !12
  store i32 2, ptr %i0.1, !dbg !12
  %i0.2 = getelementptr inbounds [3 x i32], ptr %i0, i32 0, i64 2, !dbg !12
  store i32 3, ptr %i0.2, !dbg !12
  %i1.0 = getelementptr inbounds [3 x i32], ptr %i1, i32 0, i64 0, !dbg !13
  store i32 4, ptr %i1.0, !dbg !13
  %i1.1 = getelementptr inbounds [3 x i32], ptr %i1, i32 0, i64 1, !dbg !13
  store i32 5, ptr %i1.1, !dbg !13
  %i1.2 = getelementptr inbounds [3 x i32], ptr %i1, i32 0, i64 2, !dbg !13
  store i32 6, ptr %i1.2, !dbg !13
  %i2.0 = getelementptr inbounds [2 x [3 x i32]], ptr %v0, i32 0, i64 0, !dbg !14
  call void @llvm.memcpy.p0.p0.i64(ptr %i2.0, ptr %i0, i64 12, i1 false), !dbg !14
  %i2.1 = getelementptr inbounds [2 x [3 x i32]], ptr %v0, i32 0, i64 1, !dbg !14
  call void @llvm.memcpy.p0.p0.i64(ptr %i2.1, ptr %i1, i64 12, i1 false), !dbg !14
  call void @llvm.memcpy.p0.p0.i64(ptr %i4, ptr %v0, i64 24, i1 false), !dbg !16
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.2), !dbg !17
  call void @print.array.0(ptr %i4), !dbg !17
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.4), !dbg !17
  call void () @"peeper_rt_v1_flush"(), !dbg !17
  ret void, !dbg !18
}

define private void @print.array.1(ptr %array) {
entry:
  %element.m = alloca i64
  br label %loop
loop:
  %index = phi i64 [ 0, %entry ], [ %next, %step ]
  %first = icmp eq i64 %index, 0
  %separator = select i1 %first, ptr @.str.0, ptr @.str.1
  call void (ptr) @"peeper_rt_v1_print_text"(ptr %separator)
  %address = getelementptr inbounds [3 x i32], ptr %array, i64 0, i64 %index
  %element = load i32, ptr %address
  %element.x = sext i32 %element to i64
  store i64 %element.x, ptr %element.m
  call void (i32, i32, ptr) @"peeper_rt_v1_print_int"(i32 1, i32 1, ptr %element.m)
  br label %step
step:
  %next = add i64 %index, 1
  %more = icmp ult i64 %next, 3
  br i1 %more, label %loop, label %done
done:
  ret void
}

define private void @print.array.0(ptr %array) {
entry:
  br label %loop
loop:
  %index = phi i64 [ 0, %entry ], [ %next, %step ]
  %first = icmp eq i64 %index, 0
  %separator = select i1 %first, ptr @.str.0, ptr @.str.1
  call void (ptr) @"peeper_rt_v1_print_text"(ptr %separator)
  %address = getelementptr inbounds [2 x [3 x i32]], ptr %array, i64 0, i64 %index
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.2)
  call void @print.array.1(ptr %address)
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.3)
  br label %step
step:
  %next = add i64 %index, 1
  %more = icmp ult i64 %next, 2
  br i1 %more, label %loop, label %done
done:
  ret void
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_int"(i32, i32, ptr)

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!19, !20}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DISubroutineType(types: !{null})
!3 = distinct !DISubprogram(name: "main", linkageName: "ll_print_arrays:main.main", scope: !0, file: !0, line: 3, type: !2, scopeLine: 3, spFlags: DISPFlagDefinition, unit: !1)
!4 = !DILexicalBlock(scope: !3, file: !0, line: 3, column: 11)
!5 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!6 = !DISubrange(count: 3)
!7 = !DICompositeType(tag: DW_TAG_array_type, baseType: !5, size: 96, elements: !{!6})
!8 = !DISubrange(count: 2)
!9 = !DICompositeType(tag: DW_TAG_array_type, baseType: !7, size: 192, elements: !{!8})
!10 = !DILocalVariable(name: "grid", scope: !4, file: !0, line: 4, type: !9)
!11 = !DILocation(line: 4, column: 9, scope: !4)
!12 = !DILocation(line: 4, column: 26, scope: !4)
!13 = !DILocation(line: 4, column: 43, scope: !4)
!14 = !DILocation(line: 4, column: 16, scope: !4)
!15 = !DILocation(line: 4, column: 5, scope: !4)
!16 = !DILocation(line: 5, column: 13, scope: !4)
!17 = !DILocation(line: 5, column: 5, scope: !4)
!18 = !DILocation(line: 6, column: 1, scope: !4)
!19 = !{i32 7, !"Dwarf Version", i32 4}
!20 = !{i32 2, !"Debug Info Version", i32 3}
