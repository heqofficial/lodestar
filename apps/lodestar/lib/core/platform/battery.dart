import 'package:flutter/services.dart';

/// Asks the OS to exempt Lodestar from battery optimization so the
/// background tracking service survives Doze and OEM battery managers.
///
/// Android-only: the channel is absent on iOS, where background location
/// is handled by the OS itself. Failure (denied, unsupported, stripped
/// build) is not an error — tracking still runs, just less reliably.
Future<void> requestBatteryOptimizationExemption() async {
  const channel = MethodChannel('dev.lodestar/battery');
  try {
    final exempt =
        await channel.invokeMethod<bool>('isIgnoringBatteryOptimizations') ??
        false;
    if (!exempt) {
      await channel.invokeMethod<void>('requestIgnoreBatteryOptimizations');
    }
  } on MissingPluginException {
    // iOS or a test environment — nothing to ask for.
  } on PlatformException {
    // Denied or blocked by the OEM — proceed without the exemption.
  }
}