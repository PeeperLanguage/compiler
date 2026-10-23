source_filename = "<fixture>/src/main.peep"

define void @"ll_struct:main.main"() !dbg !3 {
entry:
  %v0 = alloca { i32, i32 }
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !10, metadata !DIExpression()), !dbg !11
  %v1 = alloca { { i32, i32 }, { i32, i32 } }
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !16, metadata !DIExpression()), !dbg !17
  %v2 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v2, metadata !18, metadata !DIExpression()), !dbg !19
  %v3 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v3, metadata !20, metadata !DIExpression()), !dbg !21
  br label %bb0
bb0:
  %i0.0 = insertvalue { i32, i32 } poison, i32 1, 0, !dbg !22
  %i0 = insertvalue { i32, i32 } %i0.0, i32 2, 1, !dbg !22
  store { i32, i32 } %i0, ptr %v0, !dbg !23
  %i2 = load { i32, i32 }, ptr %v0, !dbg !24
  %i3.0 = insertvalue { i32, i32 } poison, i32 3, 0, !dbg !25
  %i3 = insertvalue { i32, i32 } %i3.0, i32 4, 1, !dbg !25
  %i4.0 = insertvalue { { i32, i32 }, { i32, i32 } } poison, { i32, i32 } %i2, 0, !dbg !26
  %i4 = insertvalue { { i32, i32 }, { i32, i32 } } %i4.0, { i32, i32 } %i3, 1, !dbg !26
  store { { i32, i32 }, { i32, i32 } } %i4, ptr %v1, !dbg !27
  %i6.f = getelementptr inbounds { { i32, i32 }, { i32, i32 } }, ptr %v1, i32 0, i32 1, i32 0, !dbg !28
  %i6 = load i32, ptr %i6.f, !dbg !28
  store i32 %i6, ptr %v2, !dbg !29
  %i8.0 = insertvalue { i32, i32 } poison, i32 5, 0, !dbg !30
  %i8 = insertvalue { i32, i32 } %i8.0, i32 6, 1, !dbg !30
  %i9 = extractvalue { i32, i32 } %i8, 1, !dbg !30
  store i32 %i9, ptr %v3, !dbg !31
  ret void, !dbg !32
}

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!33, !34}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DISubroutineType(types: !{null})
!3 = distinct !DISubprogram(name: "main", linkageName: "ll_struct:main.main", scope: !0, file: !0, line: 14, type: !2, scopeLine: 14, spFlags: DISPFlagDefinition, unit: !1)
!4 = !DILexicalBlock(scope: !3, file: !0, line: 14, column: 11)
!6 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "x", scope: !5, baseType: !6, offset: 0)
!8 = !DIDerivedType(tag: DW_TAG_member, name: "y", scope: !5, baseType: !6, offset: 32)
!9 = !{!7, !8}
!5 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Point", size: 64, elements: !9)
!10 = !DILocalVariable(name: "p", scope: !4, file: !0, line: 15, type: !5)
!11 = !DILocation(line: 15, column: 9, scope: !4)
!13 = !DIDerivedType(tag: DW_TAG_member, name: "start", scope: !12, baseType: !5, offset: 0)
!14 = !DIDerivedType(tag: DW_TAG_member, name: "end", scope: !12, baseType: !5, offset: 64)
!15 = !{!13, !14}
!12 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Segment", size: 128, elements: !15)
!16 = !DILocalVariable(name: "s", scope: !4, file: !0, line: 16, type: !12)
!17 = !DILocation(line: 16, column: 9, scope: !4)
!18 = !DILocalVariable(name: "x", scope: !4, file: !0, line: 17, type: !6)
!19 = !DILocation(line: 17, column: 9, scope: !4)
!20 = !DILocalVariable(name: "y", scope: !4, file: !0, line: 18, type: !6)
!21 = !DILocation(line: 18, column: 9, scope: !4)
!22 = !DILocation(line: 15, column: 13, scope: !4)
!23 = !DILocation(line: 15, column: 5, scope: !4)
!24 = !DILocation(line: 16, column: 31, scope: !4)
!25 = !DILocation(line: 16, column: 40, scope: !4)
!26 = !DILocation(line: 16, column: 13, scope: !4)
!27 = !DILocation(line: 16, column: 5, scope: !4)
!28 = !DILocation(line: 17, column: 13, scope: !4)
!29 = !DILocation(line: 17, column: 5, scope: !4)
!30 = !DILocation(line: 18, column: 13, scope: !4)
!31 = !DILocation(line: 18, column: 5, scope: !4)
!32 = !DILocation(line: 19, column: 1, scope: !4)
!33 = !{i32 7, !"Dwarf Version", i32 4}
!34 = !{i32 2, !"Debug Info Version", i32 3}
