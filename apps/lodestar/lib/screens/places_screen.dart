import 'package:flutter/material.dart';
import 'package:flutter_map/flutter_map.dart';
import 'package:latlong2/latlong.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';

/// Manage geofence places (Home, School, Work, …). Geofence events are
/// evaluated on-device and shared as encrypted envelopes.
class PlacesScreen extends StatelessWidget {
  const PlacesScreen({super.key});

  Future<void> _addPlace(BuildContext context) async {
    final state = context.read<AppState>();
    final place = await showModalBottomSheet<Map<String, dynamic>>(
      context: context,
      isScrollControlled: true,
      builder: (_) => const _AddPlaceSheet(),
    );
    if (place == null) return;
    await state.addPlace(
      place['name'] as String,
      (place['lat'] as num).toDouble(),
      (place['lng'] as num).toDouble(),
      (place['radius'] as num).toDouble(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final places = state.places.values.toList()
      ..sort((a, b) => a.name.compareTo(b.name));
    return Scaffold(
      appBar: AppBar(title: const Text('Places')),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => _addPlace(context),
        icon: const Icon(Icons.add_location_alt_outlined),
        label: const Text('Add place'),
      ),
      body: places.isEmpty
          ? Center(
              child: Padding(
                padding: const EdgeInsets.all(32),
                child: Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    const Icon(
                      Icons.location_on_outlined,
                      size: 64,
                      color: Colors.grey,
                    ),
                    const SizedBox(height: 12),
                    Text(
                      'No places yet',
                      style: Theme.of(context).textTheme.titleLarge,
                    ),
                    const SizedBox(height: 8),
                    Text(
                      'Add Home, School, Work… and Lodestar will alert the circle '
                      'when members arrive or leave.',
                      textAlign: TextAlign.center,
                    ),
                  ],
                ),
              ),
            )
          : ListView.separated(
              padding: const EdgeInsets.all(16),
              itemCount: places.length,
              separatorBuilder: (_, _) => const SizedBox(height: 8),
              itemBuilder: (context, i) {
                final p = places[i];
                return Card(
                  child: ListTile(
                    leading: CircleAvatar(
                      backgroundColor: const Color(0x224F7CFF),
                      child: const Icon(
                        Icons.location_on,
                        color: Color(0xFF4F7CFF),
                      ),
                    ),
                    title: Text(p.name),
                    subtitle: Text('${p.radiusM.round()} m radius'),
                    trailing: IconButton(
                      icon: const Icon(Icons.delete_outline),
                      tooltip: 'Delete place',
                      onPressed: () async {
                        // Deleting is circle-wide (everyone's geofence drops
                        // the place), so ask before the one-tap removal.
                        final ok = await showDialog<bool>(
                          context: context,
                          builder: (ctx) => AlertDialog(
                            title: const Text('Delete place?'),
                            content: Text(
                              '“${p.name}” will be removed for the whole circle.',
                            ),
                            actions: [
                              TextButton(
                                onPressed: () => Navigator.pop(ctx, false),
                                child: const Text('Cancel'),
                              ),
                              FilledButton(
                                style: FilledButton.styleFrom(
                                  backgroundColor: Colors.red,
                                ),
                                onPressed: () => Navigator.pop(ctx, true),
                                child: const Text('Delete'),
                              ),
                            ],
                          ),
                        );
                        if (ok == true) await state.deletePlace(p.id);
                      },
                    ),
                  ),
                );
              },
            ),
    );
  }
}

class _AddPlaceSheet extends StatefulWidget {
  const _AddPlaceSheet();

  @override
  State<_AddPlaceSheet> createState() => _AddPlaceSheetState();
}

class _AddPlaceSheetState extends State<_AddPlaceSheet> {
  final _nameCtrl = TextEditingController(text: 'Home');
  double _radius = 100;
  LatLng? _picked;
  bool _picking = false;
  final MapController _mapController = MapController();

  @override
  void dispose() {
    _nameCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsets.only(
        bottom: MediaQuery.of(context).viewInsets.bottom,
      ),
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Add a place', style: Theme.of(context).textTheme.titleLarge),
            const SizedBox(height: 16),
            TextField(
              controller: _nameCtrl,
              decoration: const InputDecoration(
                labelText: 'Name',
                border: OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 12),
            Text('Radius: ${_radius.round()} m'),
            Slider(
              value: _radius,
              min: 25,
              max: 2000,
              divisions: 40,
              label: '${_radius.round()} m',
              onChanged: (v) => setState(() => _radius = v),
            ),
            const SizedBox(height: 12),
            Container(
              height: 220,
              clipBehavior: Clip.antiAlias,
              decoration: BoxDecoration(
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.grey.shade300),
              ),
              child: Stack(
                children: [
                  FlutterMap(
                    mapController: _mapController,
                    options: MapOptions(
                      initialCenter: _picked ?? const LatLng(40.0, -3.7),
                      initialZoom: 15,
                      onTap: (_, point) => setState(() {
                        _picked = point;
                        _picking = false;
                      }),
                    ),
                    children: [
                      TileLayer(
                        urlTemplate:
                            'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
                        userAgentPackageName: 'dev.lodestar.app',
                        maxNativeZoom: 19,
                      ),
                      if (_picked != null)
                        MarkerLayer(
                          markers: [
                            Marker(
                              point: _picked!,
                              width: 40,
                              height: 40,
                              child: const Icon(
                                Icons.location_on,
                                color: Color(0xFFE05D5D),
                                size: 36,
                              ),
                            ),
                          ],
                        ),
                    ],
                  ),
                  if (_picking)
                    Center(
                      child: Container(
                        padding: const EdgeInsets.all(8),
                        decoration: BoxDecoration(
                          color: Colors.black87,
                          borderRadius: BorderRadius.circular(8),
                        ),
                        child: const Text(
                          'Tap the map to drop the pin',
                          style: TextStyle(color: Colors.white, fontSize: 12),
                        ),
                      ),
                    ),
                ],
              ),
            ),
            const SizedBox(height: 16),
            FilledButton.icon(
              onPressed: () {
                if (_picked == null) {
                  setState(() => _picking = true);
                  return;
                }
                Navigator.pop(context, {
                  'name': _nameCtrl.text.trim().isEmpty
                      ? 'Place'
                      : _nameCtrl.text.trim(),
                  'lat': _picked!.latitude,
                  'lng': _picked!.longitude,
                  'radius': _radius,
                });
              },
              icon: Icon(_picked == null ? Icons.touch_app : Icons.check),
              label: Text(_picked == null ? 'Pick on map' : 'Save place'),
            ),
          ],
        ),
      ),
    );
  }
}
