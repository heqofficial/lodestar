import Flutter
import UIKit
import UserNotifications

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    // Ask permission at launch; the token only materializes once granted.
    // The server-side APNs chain is fully functional, but delivery needs
    // the Push Notifications capability enabled in Xcode (creates
    // Runner.entitlements with aps-environment) — see docs/deployment.md.
    UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, _ in
      guard granted else { return }
      DispatchQueue.main.async {
        UIApplication.shared.registerForRemoteNotifications()
      }
    }
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    guard let registrar = engineBridge.pluginRegistry.registrar(forPlugin: "PushTokenPlugin") else {
      return
    }
    let channel = FlutterMethodChannel(
      name: "dev.lodestar/push", binaryMessenger: registrar.messenger())
    channel.setMethodCallHandler { call, result in
      guard call.method == "apnsToken" else {
        result(FlutterMethodNotImplemented)
        return
      }
      result(UserDefaults.standard.string(forKey: "lodestar_apns_token") ?? "")
    }
  }

  override func application(
    _ application: UIApplication,
    didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
  ) {
    // APNs tokens are opaque hex; store the latest — they rotate, and the
    // app re-syncs on launch and daily.
    let token = deviceToken.map { String(format: "%02x", $0) }.joined()
    UserDefaults.standard.set(token, forKey: "lodestar_apns_token")
  }

  override func application(
    _ application: UIApplication,
    didFailToRegisterForRemoteNotificationsWithError error: Error
  ) {
    // Simulator, no entitlement, or APNs unreachable — nothing to serve.
    UserDefaults.standard.removeObject(forKey: "lodestar_apns_token")
  }
}