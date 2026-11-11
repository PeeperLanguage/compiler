source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [1 x i8] c"["
@.str.1 = private unnamed_addr constant [2 x i8] c", "
@.str.2 = private unnamed_addr constant [2 x i8] c"]\0A"

define void @"ll_array_ranges:main.known"(ptr %a0) !dbg !7 {
entry:
  %s0 = alloca [24 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !9, metadata !DIExpression()), !dbg !10
  %s1 = alloca [4 x i8], align 4
  %s2 = alloca [8 x i8], align 8
  %s3 = alloca [4 x i8], align 4
  %s4 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store i32 0, ptr %s1, !dbg !11
  store i64 0, ptr %s2, !dbg !11
  br label %bb1
bb1:
  %i2 = load i64, ptr %s2, !dbg !11
  %i3.c = icmp ult i64 %i2, 3, !dbg !11
  br i1 %i3.c, label %bb2, label %bb3, !dbg !11
bb2:
  %i4 = load i32, ptr %s1, !dbg !11
  %i5 = getelementptr inbounds [4 x i8], ptr %s0, i64 %i2, !dbg !11
  store i32 %i4, ptr %i5, !dbg !11
  %i6 = add i32 %i4, 1, !dbg !11
  store i32 %i6, ptr %s1, !dbg !11
  %i8 = add i64 %i2, 1, !dbg !11
  store i64 %i8, ptr %s2, !dbg !11
  br label %bb1
bb3:
  %i10 = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !11
  store i32 9, ptr %i10, !dbg !11
  store i32 5, ptr %s3, !dbg !11
  store i64 0, ptr %s4, !dbg !11
  br label %bb4
bb4:
  %i13 = load i64, ptr %s4, !dbg !11
  %i14.c = icmp ult i64 %i13, 2, !dbg !11
  br i1 %i14.c, label %bb5, label %bb6, !dbg !11
bb5:
  %i15 = load i32, ptr %s3, !dbg !11
  %i16.1 = getelementptr inbounds i8, ptr %s0, i64 16, !dbg !11
  %i16 = getelementptr inbounds [4 x i8], ptr %i16.1, i64 %i13, !dbg !11
  store i32 %i15, ptr %i16, !dbg !11
  %i17 = add i32 %i15, -1, !dbg !11
  store i32 %i17, ptr %s3, !dbg !11
  %i19 = add i64 %i13, 1, !dbg !11
  store i64 %i19, ptr %s4, !dbg !11
  br label %bb4
bb6:
  call void @llvm.memcpy.p0.p0.i64(ptr %a0, ptr %s0, i64 24, i1 false), !dbg !12
  ret void, !dbg !13
}

define void @"ll_array_ranges:main.main"() !dbg !15 {
entry:
  %s0 = alloca [24 x i8], align 4
  %s1 = alloca [8 x i8], align 8
  %s2 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  call void @"ll_array_ranges:main.known"(ptr %s0), !dbg !16
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !17
  store i64 0, ptr %s1, !dbg !17
  br label %bb1
bb1:
  %i4 = load i64, ptr %s1, !dbg !17
  %i5.c = icmp eq i64 %i4, 0, !dbg !17
  br i1 %i5.c, label %bb3, label %bb2, !dbg !17
bb2:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 2), !dbg !17
  br label %bb3
bb3:
  %i7.a = getelementptr inbounds [4 x i8], ptr %s0, i64 %i4, !dbg !17
  %i7 = load i32, ptr %i7.a, !dbg !17
  %i8 = sext i32 %i7 to i64, !dbg !17
  store i64 %i8, ptr %s2, !dbg !17
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s2), !dbg !17
  %i12 = add i64 %i4, 1, !dbg !17
  store i64 %i12, ptr %s1, !dbg !17
  %i14.c = icmp ult i64 %i12, 6, !dbg !17
  br i1 %i14.c, label %bb1, label %bb4, !dbg !17
bb4:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.2, i64 2), !dbg !17
  call void @"peeper_rt_v1_flush"(), !dbg !17
  ret void, !dbg !18
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!19, !20}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!4 = !DISubrange(count: 6)
!2 = !DICompositeType(tag: DW_TAG_array_type, baseType: !3, size: 192, elements: !{!4})
!3 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!5 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!6 = !DISubroutineType(types: !{null, !5})
!7 = distinct !DISubprogram(name: "known", linkageName: "ll_array_ranges:main.known", scope: !0, file: !0, line: 3, type: !6, scopeLine: 3, spFlags: DISPFlagDefinition, unit: !1)
!8 = !DILexicalBlock(scope: !7, file: !0, line: 3, column: 22)
!9 = !DILocalVariable(name: "arr", scope: !8, file: !0, line: 4, type: !2)
!10 = !DILocation(line: 4, column: 9, scope: !8)
!11 = !DILocation(line: 4, column: 15, scope: !8)
!12 = !DILocation(line: 5, column: 12, scope: !8)
!13 = !DILocation(line: 5, column: 5, scope: !8)
!14 = !DISubroutineType(types: !{null})
!15 = distinct !DISubprogram(name: "main", linkageName: "ll_array_ranges:main.main", scope: !0, file: !0, line: 8, type: !14, scopeLine: 8, spFlags: DISPFlagDefinition, unit: !1)
!16 = !DILocation(line: 9, column: 13, scope: !15)
!17 = !DILocation(line: 9, column: 5, scope: !15)
!18 = !DILocation(line: 10, column: 1, scope: !15)
!19 = !{i32 7, !"Dwarf Version", i32 4}
!20 = !{i32 2, !"Debug Info Version", i32 3}
