import Foundation
import Virtualization

// Configuration validation requires the same process entitlement as VM startup.
// No VM is started and no privileged or host networking changes are made.
if !VZVirtualMachine.isSupported {
    print("::notice::SKIP VM configuration smoke test: this host does not expose virtualization hardware. Executable entitlement checks remain mandatory.")
    exit(0)
}
let kernel = URL(fileURLWithPath: CommandLine.arguments[1])
let configuration = VZVirtualMachineConfiguration()
configuration.bootLoader = VZLinuxBootLoader(kernelURL: kernel)
configuration.cpuCount = VZVirtualMachineConfiguration.minimumAllowedCPUCount
configuration.memorySize = max(VZVirtualMachineConfiguration.minimumAllowedMemorySize, 512 * 1024 * 1024)
do {
    try configuration.validate()
    print("Virtualization configuration validation succeeded")
} catch {
    fputs("\(error)\n", stderr)
    exit(1)
}
