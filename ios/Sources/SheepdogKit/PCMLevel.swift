import Foundation

/// Sample-level measurements shared by the capture path, the detector and
/// the microphone watchdog.
public enum PCMLevel {
    /// Largest absolute sample of a frame, in LSB.
    public static func peak(of frame: [Int16]) -> Int {
        var p = 0
        for s in frame {
            let a = s == Int16.min ? 32_768 : abs(Int(s))
            if a > p { p = a }
        }
        return p
    }
}
