source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [66 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:6:12\0A\00"
@.str.1 = private unnamed_addr constant [66 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:10:5\0A\00"

define i32 @"ll_slices:main.get"(%slice %arg0, i64 %arg1) !dbg !11 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !12, metadata !DIExpression()), !dbg !13
  %v1 = alloca i64
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !14, metadata !DIExpression()), !dbg !15
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !13
  store i64 %arg1, ptr %v1, !dbg !15
  %i2 = load i64, ptr %v1, !dbg !16
  %i3.s = load %slice, ptr %v0, !dbg !17
  %i3 = extractvalue %slice %i3.s, 1, !dbg !17
  %i4 = icmp uge i64 %i2, %i3, !dbg !17
  br i1 %i4, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i6.f.0 = load ptr, ptr %v0, !dbg !17
  %i6.f = getelementptr inbounds i32, ptr %i6.f.0, i64 %i2, !dbg !17
  %i6 = load i32, ptr %i6.f, !dbg !17
  %i7 = load %slice, ptr %v0, !dbg !18
  %i8 = extractvalue %slice %i7, 1, !dbg !18
  %i9 = trunc i64 %i8 to i32, !dbg !18
  %i10 = add i32 %i6, %i9, !dbg !17
  ret i32 %i10, !dbg !19
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 65), !dbg !17
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !17
  unreachable
}

define void @"ll_slices:main.set"(%slice %arg0) !dbg !27 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !28, metadata !DIExpression()), !dbg !29
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !29
  %i1.s = load %slice, ptr %v0, !dbg !30
  %i1 = extractvalue %slice %i1.s, 1, !dbg !30
  %i2 = icmp uge i64 0, %i1, !dbg !30
  br i1 %i2, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i4.f.0 = load ptr, ptr %v0, !dbg !30
  %i4.f = getelementptr inbounds i32, ptr %i4.f.0, i64 0, !dbg !30
  store i32 1, ptr %i4.f, !dbg !30
  ret void, !dbg !31
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 65), !dbg !30
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !30
  unreachable
}

define void @"ll_slices:main.lend"(%slice %arg0) !dbg !33 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !34, metadata !DIExpression()), !dbg !35
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !35
  %i1 = load %slice, ptr %v0, !dbg !36
  call void @"ll_slices:main.set"(%slice %i1), !dbg !37
  ret void, !dbg !38
}

define void @"ll_slices:main.main"() !dbg !40 {
entry:
  %v0 = alloca [8 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !44, metadata !DIExpression()), !dbg !45
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !46, metadata !DIExpression()), !dbg !47
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds i8, ptr %v0, i64 0, !dbg !48
  store i32 3, ptr %i0.0, !dbg !48
  %i0.1 = getelementptr inbounds i8, ptr %v0, i64 4, !dbg !48
  store i32 4, ptr %i0.1, !dbg !48
  %i2.p = getelementptr inbounds i8, ptr %v0, i64 0, !dbg !50
  %i2.a = insertvalue %slice poison, ptr %i2.p, 0, !dbg !50
  %i2 = insertvalue %slice %i2.a, i64 2, 1, !dbg !50
  call void @"ll_slices:main.set"(%slice %i2), !dbg !51
  %i4.p = getelementptr inbounds i8, ptr %v0, i64 0, !dbg !52
  %i4.a = insertvalue %slice poison, ptr %i4.p, 0, !dbg !52
  %i4 = insertvalue %slice %i4.a, i64 2, 1, !dbg !52
  call void @"ll_slices:main.lend"(%slice %i4), !dbg !53
  %i6.p = getelementptr inbounds i8, ptr %v0, i64 0, !dbg !54
  %i6.a = insertvalue %slice poison, ptr %i6.p, 0, !dbg !54
  %i6 = insertvalue %slice %i6.a, i64 2, 1, !dbg !54
  %i7 = call i32 @"ll_slices:main.get"(%slice %i6, i64 1), !dbg !55
  store i32 %i7, ptr %v1, !dbg !56
  ret void, !dbg !57
}

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!58, !59}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!5 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!6 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !5, offset: 64)
!8 = !{!6, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&[]i32", size: 128, elements: !8)
!9 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!10 = !DISubroutineType(types: !{!2, !3, !9})
!11 = distinct !DISubprogram(name: "get", linkageName: "ll_slices:main.get", scope: !0, file: !0, line: 5, type: !10, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!12 = !DILocalVariable(name: "view", arg: 1, scope: !11, file: !0, line: 5, type: !3)
!13 = !DILocation(line: 5, column: 8, scope: !11)
!14 = !DILocalVariable(name: "at", arg: 2, scope: !11, file: !0, line: 5, type: !9)
!15 = !DILocation(line: 5, column: 22, scope: !11)
!16 = !DILocation(line: 6, column: 17, scope: !11)
!17 = !DILocation(line: 6, column: 12, scope: !11)
!18 = !DILocation(line: 6, column: 23, scope: !11)
!19 = !DILocation(line: 6, column: 5, scope: !11)
!21 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!22 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!23 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !20, baseType: !21, offset: 0)
!24 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !20, baseType: !22, offset: 64)
!25 = !{!23, !24}
!20 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "&mut []i32", size: 128, elements: !25)
!26 = !DISubroutineType(types: !{null, !20})
!27 = distinct !DISubprogram(name: "set", linkageName: "ll_slices:main.set", scope: !0, file: !0, line: 9, type: !26, scopeLine: 9, spFlags: DISPFlagDefinition, unit: !1)
!28 = !DILocalVariable(name: "edit", arg: 1, scope: !27, file: !0, line: 9, type: !20)
!29 = !DILocation(line: 9, column: 8, scope: !27)
!30 = !DILocation(line: 10, column: 5, scope: !27)
!31 = !DILocation(line: 11, column: 1, scope: !27)
!32 = !DISubroutineType(types: !{null, !20})
!33 = distinct !DISubprogram(name: "lend", linkageName: "ll_slices:main.lend", scope: !0, file: !0, line: 13, type: !32, scopeLine: 13, spFlags: DISPFlagDefinition, unit: !1)
!34 = !DILocalVariable(name: "edit", arg: 1, scope: !33, file: !0, line: 13, type: !20)
!35 = !DILocation(line: 13, column: 9, scope: !33)
!36 = !DILocation(line: 14, column: 9, scope: !33)
!37 = !DILocation(line: 14, column: 5, scope: !33)
!38 = !DILocation(line: 15, column: 1, scope: !33)
!39 = !DISubroutineType(types: !{null})
!40 = distinct !DISubprogram(name: "main", linkageName: "ll_slices:main.main", scope: !0, file: !0, line: 17, type: !39, scopeLine: 17, spFlags: DISPFlagDefinition, unit: !1)
!41 = !DILexicalBlock(scope: !40, file: !0, line: 17, column: 11)
!42 = !DISubrange(count: 2)
!43 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 64, elements: !{!42})
!44 = !DILocalVariable(name: "values", scope: !41, file: !0, line: 18, type: !43)
!45 = !DILocation(line: 18, column: 13, scope: !41)
!46 = !DILocalVariable(name: "read", scope: !41, file: !0, line: 21, type: !2)
!47 = !DILocation(line: 21, column: 9, scope: !41)
!48 = !DILocation(line: 18, column: 22, scope: !41)
!49 = !DILocation(line: 18, column: 5, scope: !41)
!50 = !DILocation(line: 19, column: 9, scope: !41)
!51 = !DILocation(line: 19, column: 5, scope: !41)
!52 = !DILocation(line: 20, column: 10, scope: !41)
!53 = !DILocation(line: 20, column: 5, scope: !41)
!54 = !DILocation(line: 21, column: 20, scope: !41)
!55 = !DILocation(line: 21, column: 16, scope: !41)
!56 = !DILocation(line: 21, column: 5, scope: !41)
!57 = !DILocation(line: 22, column: 1, scope: !41)
!58 = !{i32 7, !"Dwarf Version", i32 4}
!59 = !{i32 2, !"Debug Info Version", i32 3}
