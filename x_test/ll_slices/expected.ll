source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [65 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:6:12\0A"
@.str.1 = private unnamed_addr constant [65 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:10:5\0A"

define i32 @"ll_slices:main.get"(ptr %a0, i64 %a1) !dbg !12 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a0, metadata !13, metadata !DIExpression()), !dbg !14
  %s1 = alloca [8 x i8], align 8
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !15, metadata !DIExpression()), !dbg !16
  br label %bb0
bb0:
  store i64 %a1, ptr %s1, !dbg !16
  %i1 = load i64, ptr %s1, !dbg !17
  %i2.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !18
  %i2 = load i64, ptr %i2.a, !dbg !18
  %i3.c = icmp ule i64 %i2, %i1, !dbg !18
  br i1 %i3.c, label %bb0.1.panic, label %bb0.1, !dbg !18
bb0.1:
  %i5.a.1 = load ptr, ptr %a0, !dbg !18
  %i5.a = getelementptr inbounds [4 x i8], ptr %i5.a.1, i64 %i1, !dbg !18
  %i5 = load i32, ptr %i5.a, !dbg !18
  %i6.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !19
  %i6 = load i64, ptr %i6.a, !dbg !19
  %i7 = trunc i64 %i6 to i32, !dbg !19
  %i8 = add i32 %i5, %i7, !dbg !18
  ret i32 %i8, !dbg !20
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 65), !dbg !18
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !18
  unreachable
}

define void @"ll_slices:main.set"(ptr %a0) !dbg !29 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a0, metadata !30, metadata !DIExpression()), !dbg !31
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !32
  %i0 = load i64, ptr %i0.a, !dbg !32
  %i1.c = icmp ule i64 %i0, 0, !dbg !32
  br i1 %i1.c, label %bb0.1.panic, label %bb0.1, !dbg !32
bb0.1:
  %i3 = load ptr, ptr %a0, !dbg !32
  store i32 1, ptr %i3, !dbg !32
  ret void, !dbg !33
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 65), !dbg !32
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !32
  unreachable
}

define void @"ll_slices:main.lend"(ptr %a0) !dbg !36 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a0, metadata !37, metadata !DIExpression()), !dbg !38
  %s1 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a0, i64 8, !dbg !39
  %i0 = load i64, ptr %i0.a, !dbg !39
  %i1 = load ptr, ptr %a0, !dbg !39
  store ptr %i1, ptr %s1, !dbg !39
  %i3 = getelementptr inbounds i8, ptr %s1, i64 8, !dbg !39
  store i64 %i0, ptr %i3, !dbg !39
  call void @"ll_slices:main.set"(ptr %s1), !dbg !40
  ret void, !dbg !41
}

define void @"ll_slices:main.main"() !dbg !43 {
entry:
  %s0 = alloca [8 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !47, metadata !DIExpression()), !dbg !48
  %s1 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !49, metadata !DIExpression()), !dbg !50
  %s2 = alloca [16 x i8], align 8
  %s3 = alloca [16 x i8], align 8
  %s4 = alloca [16 x i8], align 8
  br label %bb0
bb0:
  store i32 3, ptr %s0, !dbg !51
  %i1 = getelementptr inbounds i8, ptr %s0, i64 4, !dbg !51
  store i32 4, ptr %i1, !dbg !51
  store ptr %s0, ptr %s2, !dbg !52
  %i4 = getelementptr inbounds i8, ptr %s2, i64 8, !dbg !52
  store i64 2, ptr %i4, !dbg !52
  call void @"ll_slices:main.set"(ptr %s2), !dbg !53
  store ptr %s0, ptr %s3, !dbg !54
  %i10 = getelementptr inbounds i8, ptr %s3, i64 8, !dbg !54
  store i64 2, ptr %i10, !dbg !54
  call void @"ll_slices:main.lend"(ptr %s3), !dbg !55
  store ptr %s0, ptr %s4, !dbg !56
  %i16 = getelementptr inbounds i8, ptr %s4, i64 8, !dbg !56
  store i64 2, ptr %i16, !dbg !56
  %i18 = call i32 @"ll_slices:main.get"(ptr %s4, i64 1), !dbg !57
  store i32 %i18, ptr %s1, !dbg !57
  ret void, !dbg !58
}

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!59, !60}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!5 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !6, offset: 64)
!8 = !{!5, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&[]i32", size: 128, elements: !8)
!6 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!9 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !3, size: 64)
!10 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!11 = !DISubroutineType(types: !{!2, !9, !10})
!12 = distinct !DISubprogram(name: "get", linkageName: "ll_slices:main.get", scope: !0, file: !0, line: 5, type: !11, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!13 = !DILocalVariable(name: "view", arg: 1, scope: !12, file: !0, line: 5, type: !3)
!14 = !DILocation(line: 5, column: 8, scope: !12)
!15 = !DILocalVariable(name: "at", arg: 2, scope: !12, file: !0, line: 5, type: !10)
!16 = !DILocation(line: 5, column: 22, scope: !12)
!17 = !DILocation(line: 6, column: 17, scope: !12)
!18 = !DILocation(line: 6, column: 12, scope: !12)
!19 = !DILocation(line: 6, column: 23, scope: !12)
!20 = !DILocation(line: 6, column: 5, scope: !12)
!23 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !21, baseType: !22, offset: 0)
!25 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !21, baseType: !24, offset: 64)
!26 = !{!23, !25}
!21 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&mut []i32", size: 128, elements: !26)
!24 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!22 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!27 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !21, size: 64)
!28 = !DISubroutineType(types: !{null, !27})
!29 = distinct !DISubprogram(name: "set", linkageName: "ll_slices:main.set", scope: !0, file: !0, line: 9, type: !28, scopeLine: 9, spFlags: DISPFlagDefinition, unit: !1)
!30 = !DILocalVariable(name: "edit", arg: 1, scope: !29, file: !0, line: 9, type: !21)
!31 = !DILocation(line: 9, column: 8, scope: !29)
!32 = !DILocation(line: 10, column: 5, scope: !29)
!33 = !DILocation(line: 11, column: 1, scope: !29)
!34 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !21, size: 64)
!35 = !DISubroutineType(types: !{null, !34})
!36 = distinct !DISubprogram(name: "lend", linkageName: "ll_slices:main.lend", scope: !0, file: !0, line: 13, type: !35, scopeLine: 13, spFlags: DISPFlagDefinition, unit: !1)
!37 = !DILocalVariable(name: "edit", arg: 1, scope: !36, file: !0, line: 13, type: !21)
!38 = !DILocation(line: 13, column: 9, scope: !36)
!39 = !DILocation(line: 14, column: 9, scope: !36)
!40 = !DILocation(line: 14, column: 5, scope: !36)
!41 = !DILocation(line: 15, column: 1, scope: !36)
!42 = !DISubroutineType(types: !{null})
!43 = distinct !DISubprogram(name: "main", linkageName: "ll_slices:main.main", scope: !0, file: !0, line: 17, type: !42, scopeLine: 17, spFlags: DISPFlagDefinition, unit: !1)
!44 = !DILexicalBlock(scope: !43, file: !0, line: 17, column: 11)
!46 = !DISubrange(count: 2)
!45 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 64, elements: !{!46})
!47 = !DILocalVariable(name: "values", scope: !44, file: !0, line: 18, type: !45)
!48 = !DILocation(line: 18, column: 13, scope: !44)
!49 = !DILocalVariable(name: "read", scope: !44, file: !0, line: 21, type: !2)
!50 = !DILocation(line: 21, column: 9, scope: !44)
!51 = !DILocation(line: 18, column: 22, scope: !44)
!52 = !DILocation(line: 19, column: 9, scope: !44)
!53 = !DILocation(line: 19, column: 5, scope: !44)
!54 = !DILocation(line: 20, column: 10, scope: !44)
!55 = !DILocation(line: 20, column: 5, scope: !44)
!56 = !DILocation(line: 21, column: 20, scope: !44)
!57 = !DILocation(line: 21, column: 16, scope: !44)
!58 = !DILocation(line: 22, column: 1, scope: !44)
!59 = !{i32 7, !"Dwarf Version", i32 4}
!60 = !{i32 2, !"Debug Info Version", i32 3}
