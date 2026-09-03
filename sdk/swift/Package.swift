// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "AgentContinuity",
    platforms: [.iOS(.v15), .macOS(.v12)],
    products: [
        .library(name: "AgentContinuity", targets: ["AgentContinuity"]),
        .executable(name: "AgentContinuitySelfTest", targets: ["AgentContinuitySelfTest"]),
    ],
    targets: [
        .target(name: "AgentContinuity"),
        .executableTarget(name: "AgentContinuitySelfTest", dependencies: ["AgentContinuity"], path: "Tests/AgentContinuityTests"),
    ]
)
