import 'dart:io';

import 'package:flutter/services.dart';

/// Bridges the native APNs device token to Dart.
///
/// Android has no APNs — push there rides the ntfy app — so this is a
/// no-op on Android. On iOS the native side requests notification
/// permission at launch and stores the latest token; reading it here is
/// cheap and safe at any time (the token is a cached string, not a live
/// registration). A failed read is not an error: it just means no token
/// exists yet (permission denied, simulator, or a build without the Push
/// Notifications capability).
class PushTokenService {
  PushTokenService._();

  static const _channel = MethodChannel('dev.lodestar/push');

  /// Overridable in tests; the host platform is not iOS on CI.
  static bool isIos = Platform.isIOS;

  static Future<String> apnsToken() async {
    if (!isIos) return '';
    try {
      return await _channel.invokeMethod<String>('apnsToken') ?? '';
    } on PlatformException {
      return '';
    } on MissingPluginException {
      return '';
    }
  }
}