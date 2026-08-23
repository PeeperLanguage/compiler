package peeper

// CompilerVersion is canonical current Peeper compiler version.
const CompilerVersion = "0.1.0"

// SourceExt is canonical source file extension shared across compiler layers.
const SourceExt = ".peep"

// SourceDirName is canonical source directory name for config-backed projects and bundled stdlib packages.
const SourceDirName = "src"

// MainFileName is canonical default program entry file within SourceDirName.
const MainFileName = "main" + SourceExt
