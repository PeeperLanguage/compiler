source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [1 x i8] c"["
@.str.1 = private unnamed_addr constant [2 x i8] c", "
@.str.2 = private unnamed_addr constant [1 x i8] c"]"
@.str.3 = private unnamed_addr constant [2 x i8] c"]\0A"

define void @"ll_print_arrays:main.main"() !dbg !3 {
entry:
  %s0 = alloca [24 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !9, metadata !DIExpression()), !dbg !10
  %s1 = alloca [12 x i8], align 4
  %s2 = alloca [12 x i8], align 4
  %s3 = alloca [8 x i8], align 8
  %s4 = alloca [8 x i8], align 8
  %s5 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store i32 1, ptr %s1, !dbg !11
  %i1 = getelementptr inbounds i8, ptr %s1, i64 4, !dbg !11
  store i32 2, ptr %i1, !dbg !11
  %i2 = getelementptr inbounds i8, ptr %s1, i64 8, !dbg !11
  store i32 3, ptr %i2, !dbg !11
  store i32 4, ptr %s2, !dbg !12
  %i4 = getelementptr inbounds i8, ptr %s2, i64 4, !dbg !12
  store i32 5, ptr %i4, !dbg !12
  %i5 = getelementptr inbounds i8, ptr %s2, i64 8, !dbg !12
  store i32 6, ptr %i5, !dbg !12
  call void @llvm.memcpy.p0.p0.i64(ptr %s0, ptr %s1, i64 12, i1 false), !dbg !13
  %i7.t = getelementptr inbounds i8, ptr %s0, i64 12, !dbg !13
  call void @llvm.memcpy.p0.p0.i64(ptr %i7.t, ptr %s2, i64 12, i1 false), !dbg !13
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !14
  store i64 0, ptr %s3, !dbg !14
  br label %bb1
bb1:
  %i12 = load i64, ptr %s3, !dbg !14
  %i13.c = icmp eq i64 %i12, 0, !dbg !14
  br i1 %i13.c, label %bb3, label %bb2, !dbg !14
bb2:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 2), !dbg !14
  br label %bb3
bb3:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.0, i64 1), !dbg !14
  store i64 0, ptr %s4, !dbg !14
  br label %bb5
bb4:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.3, i64 2), !dbg !14
  call void @"peeper_rt_v1_flush"(), !dbg !14
  ret void, !dbg !15
bb5:
  %i17 = load i64, ptr %s4, !dbg !14
  %i18.c = icmp eq i64 %i17, 0, !dbg !14
  br i1 %i18.c, label %bb7, label %bb6, !dbg !14
bb6:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.1, i64 2), !dbg !14
  br label %bb7
bb7:
  %i20.a.1 = getelementptr inbounds [12 x i8], ptr %s0, i64 %i12, !dbg !14
  %i20.a = getelementptr inbounds [4 x i8], ptr %i20.a.1, i64 %i17, !dbg !14
  %i20 = load i32, ptr %i20.a, !dbg !14
  %i21 = sext i32 %i20 to i64, !dbg !14
  store i64 %i21, ptr %s5, !dbg !14
  call void @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %s5), !dbg !14
  %i25 = add i64 %i17, 1, !dbg !14
  store i64 %i25, ptr %s4, !dbg !14
  %i27.c = icmp ult i64 %i25, 3, !dbg !14
  br i1 %i27.c, label %bb5, label %bb8, !dbg !14
bb8:
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.2, i64 1), !dbg !14
  %i29 = add i64 %i12, 1, !dbg !14
  store i64 %i29, ptr %s3, !dbg !14
  %i31.c = icmp ult i64 %i29, 2, !dbg !14
  br i1 %i31.c, label %bb1, label %bb4, !dbg !14
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!16, !17}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DISubroutineType(types: !{null})
!3 = distinct !DISubprogram(name: "main", linkageName: "ll_print_arrays:main.main", scope: !0, file: !0, line: 3, type: !2, scopeLine: 3, spFlags: DISPFlagDefinition, unit: !1)
!4 = !DILexicalBlock(scope: !3, file: !0, line: 3, column: 11)
!7 = !DISubrange(count: 2)
!8 = !DISubrange(count: 3)
!5 = !DICompositeType(tag: DW_TAG_array_type, baseType: !6, size: 192, elements: !{!7, !8})
!6 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!9 = !DILocalVariable(name: "grid", scope: !4, file: !0, line: 4, type: !5)
!10 = !DILocation(line: 4, column: 9, scope: !4)
!11 = !DILocation(line: 4, column: 26, scope: !4)
!12 = !DILocation(line: 4, column: 43, scope: !4)
!13 = !DILocation(line: 4, column: 16, scope: !4)
!14 = !DILocation(line: 5, column: 5, scope: !4)
!15 = !DILocation(line: 6, column: 1, scope: !4)
!16 = !{i32 7, !"Dwarf Version", i32 4}
!17 = !{i32 2, !"Debug Info Version", i32 3}
