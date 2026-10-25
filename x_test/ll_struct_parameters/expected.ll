source_filename = "<fixture>/src/main.peep"

define void @"ll_struct_parameters:main.swap"(ptr %result, ptr %v0) !dbg !10 {
entry:
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !11, metadata !DIExpression()), !dbg !12
  br label %bb0
bb0:
  %i1.f = getelementptr inbounds { i32, i32 }, ptr %v0, i32 0, i32 1, !dbg !13
  %i1 = load i32, ptr %i1.f, !dbg !13
  %i2.f = getelementptr inbounds { i32, i32 }, ptr %v0, i32 0, i32 0, !dbg !14
  %i2 = load i32, ptr %i2.f, !dbg !14
  %i3.0 = getelementptr inbounds { i32, i32 }, ptr %result, i32 0, i32 0, !dbg !15
  store i32 %i1, ptr %i3.0, !dbg !15
  %i3.1 = getelementptr inbounds { i32, i32 }, ptr %result, i32 0, i32 1, !dbg !15
  store i32 %i2, ptr %i3.1, !dbg !15
  ret void, !dbg !16
bb1:
  unreachable
}

define void @"ll_struct_parameters:main.main"() !dbg !18 {
entry:
  %v0 = alloca { i32, i32 }
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !20, metadata !DIExpression()), !dbg !21
  %i0 = alloca { i32, i32 }
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds { i32, i32 }, ptr %i0, i32 0, i32 0, !dbg !22
  store i32 1, ptr %i0.0, !dbg !22
  %i0.1 = getelementptr inbounds { i32, i32 }, ptr %i0, i32 0, i32 1, !dbg !22
  store i32 2, ptr %i0.1, !dbg !22
  call void @"ll_struct_parameters:main.swap"(ptr %v0, ptr %i0), !dbg !23
  ret void, !dbg !25
}

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!26, !27}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!3 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!4 = !DIDerivedType(tag: DW_TAG_member, name: "x", scope: !2, baseType: !3, offset: 0)
!5 = !DIDerivedType(tag: DW_TAG_member, name: "y", scope: !2, baseType: !3, offset: 32)
!6 = !{!4, !5}
!2 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Point", size: 64, elements: !6)
!7 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!8 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!9 = !DISubroutineType(types: !{null, !7, !8})
!10 = distinct !DISubprogram(name: "swap", linkageName: "ll_struct_parameters:main.swap", scope: !0, file: !0, line: 7, type: !9, scopeLine: 7, spFlags: DISPFlagDefinition, unit: !1)
!11 = !DILocalVariable(name: "p", arg: 1, scope: !10, file: !0, line: 7, type: !2)
!12 = !DILocation(line: 7, column: 9, scope: !10)
!13 = !DILocation(line: 8, column: 24, scope: !10)
!14 = !DILocation(line: 8, column: 33, scope: !10)
!15 = !DILocation(line: 8, column: 12, scope: !10)
!16 = !DILocation(line: 8, column: 5, scope: !10)
!17 = !DISubroutineType(types: !{null})
!18 = distinct !DISubprogram(name: "main", linkageName: "ll_struct_parameters:main.main", scope: !0, file: !0, line: 11, type: !17, scopeLine: 11, spFlags: DISPFlagDefinition, unit: !1)
!19 = !DILexicalBlock(scope: !18, file: !0, line: 11, column: 11)
!20 = !DILocalVariable(name: "p", scope: !19, file: !0, line: 12, type: !2)
!21 = !DILocation(line: 12, column: 9, scope: !19)
!22 = !DILocation(line: 12, column: 18, scope: !19)
!23 = !DILocation(line: 12, column: 13, scope: !19)
!24 = !DILocation(line: 12, column: 5, scope: !19)
!25 = !DILocation(line: 13, column: 1, scope: !19)
!26 = !{i32 7, !"Dwarf Version", i32 4}
!27 = !{i32 2, !"Debug Info Version", i32 3}
